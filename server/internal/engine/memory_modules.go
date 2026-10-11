package engine

// Память для модулей и внешних агентов. Модуль просит области в манифесте
// (`memory`), человек выдаёт их тумблерами при установке или с экрана
// «Память»; в песочницу кладётся sema.memory.* только по выданным областям.
// Внешний агент (Claude Code, Codex) приходит по MCP, заводится как
// потребитель с пустым набором областей и ждёт выдачи.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// moduleConsumer — потребитель модуля; выданные области синхронизируются с
// манифестом при каждом запуске, чтобы отзыв на экране действовал сразу.
func (e *Engine) moduleConsumer(m *Module) *store.MemoryConsumer {
	c, err := e.store.MemoryConsumer(store.ConsumerModule, m.Title, "")
	if err != nil {
		return &store.MemoryConsumer{Kind: store.ConsumerModule, Name: m.Title}
	}
	if strings.Join(c.Areas, ",") != strings.Join(m.GrantedMemory, ",") {
		_ = e.store.SetConsumerAreas(c.ID, m.GrantedMemory)
		c.Areas = m.GrantedMemory
	}
	return c
}

// memoryTools — sema.memory.recall / remember / observe для одного запуска.
func (e *Engine) memoryTools(m *Module, who string) []script.Tool {
	origin := func() Origin {
		return Origin{Consumer: e.moduleConsumer(m), Agent: who}
	}
	return []script.Tool{
		{
			Name: "memory.recall", Need: "memory", Args: "(query)",
			Desc: "вспомнить из выданных областей памяти: уроки, факты, сводки, связи",
			Fn: func(a []any) (any, error) {
				o := origin()
				r := e.Recall(context.Background(), o, "", script.Str(a, 0))
				e.LogRecall(o, r, 0, "экономия не считается")
				var out []map[string]any
				for _, it := range r.Items {
					out = append(out, map[string]any{"kind": it.Kind, "area": it.Area, "text": it.Text, "date": shortDate(it.Created)})
				}
				return map[string]any{"items": out, "graph": r.Graph, "closed": r.Closed}, nil
			},
		},
		{
			Name: "memory.remember", Need: "memory", Args: "(area, text)",
			Desc: "запомнить факт в выданную область; повтор закрепляет урок",
			Fn: func(a []any) (any, error) {
				rec, err := e.Remember(context.Background(), origin(), MemoryRecord{Kind: store.MemFact,
					Area: script.Str(a, 0), Text: script.Str(a, 1)})
				if err != nil {
					return nil, err
				}
				return map[string]any{"id": rec.ID, "kind": rec.Kind, "status": rec.Status}, nil
			},
		},
		{
			Name: "memory.observe", Need: "memory", Args: "(area, text)",
			Desc: "записать наблюдение: живёт недолго, из него сведение делает факты",
			Fn: func(a []any) (any, error) {
				rec, err := e.Remember(context.Background(), origin(), MemoryRecord{Kind: store.MemObservation,
					Area: script.Str(a, 0), Text: script.Str(a, 1)})
				if err != nil {
					return nil, err
				}
				return rec.ID, nil
			},
		},
	}
}

// ModuleMemoryStub — те же имена для скриптов человека и чата: памяти у них
// нет, и они об этом узнают словами.
func memoryStubTools() []script.Tool {
	deny := func(a []any) (any, error) {
		return nil, fmt.Errorf("память доступна только модулям с выданными областями: оформите это модулем")
	}
	return []script.Tool{
		{Name: "memory.recall", Need: "memory", Args: "(query)", Desc: "память — только в модуле с выданными областями", Fn: deny},
		{Name: "memory.remember", Need: "memory", Args: "(area, text)", Desc: "запомнить — только в модуле", Fn: deny},
	}
}

// ---- внешние агенты ----

// ExternalOrigin — потребитель по MCP: имя клиента и рабочая папка.
func (e *Engine) ExternalOrigin(agent, path string) Origin {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		agent = "агент"
	}
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	// подпапка известного проекта — тот же потребитель: права выдаются проекту
	c, ok := e.store.MemoryConsumerByPrefix(store.ConsumerMCP, agent, path)
	if !ok {
		var err error
		c, err = e.store.MemoryConsumer(store.ConsumerMCP, agent, path)
		if err != nil {
			c = &store.MemoryConsumer{Kind: store.ConsumerMCP, Name: agent, Path: path}
		}
	}
	path = c.Path
	label := agent
	if path != "" {
		label += " · " + shortPath(path)
	}
	return Origin{Consumer: c, Agent: label}
}

func shortPath(p string) string {
	if i := strings.LastIndex(strings.TrimRight(p, "/"), "/"); i >= 0 {
		return "…/" + strings.TrimRight(p, "/")[i+1:]
	}
	return p
}

// SessionStart открывает сессию внешнего агента и отдаёт ему контекст:
// уроки, факты и кристалл прошлой сессии в пределах лимита.
func (e *Engine) SessionStart(ctx context.Context, agent, path, model, hint string, resume bool) (sessionID int64, text string, r Recalled) {
	prevSummary := ""
	model = externalModel(model)
	o := e.ExternalOrigin(agent, path)
	var sid int64
	// возобновление или сжатие контекста — та же сессия, а не новая
	if open := e.store.OpenMemorySession(o.Consumer.ID); open != nil && (resume || time.Since(sessionTime(open.Started)) < 10*time.Minute) {
		sid = open.ID
		_ = e.store.AppendSessionSummary(sid, "", 8000)
	} else {
		// прошлая сессия: её сводку агент получит кристаллом вместо того,
		// чтобы перечитывать переписку — это и есть сбережённое
		if prev := e.store.LastMemorySession(o.Consumer.ID); prev != nil && prev.Ended != "" {
			prevSummary = prev.Summary
		}
		if open := e.store.OpenMemorySession(o.Consumer.ID); open != nil {
			_ = e.store.EndMemorySession(open.ID, open.Summary) // прежняя не закрылась — закрываем
		}
		var err error
		sid, err = e.store.StartMemorySession(o.Consumer.ID, o.Agent, model)
		if err != nil {
			sid = 0
		}
	}
	o.SessionID = sid
	if len(o.Consumer.Areas) == 0 {
		e.store.LogMemory(store.MemoryLogEntry{Consumer: o.Consumer.Label(), SessionID: sid, Action: "deny",
			Detail: "начало сессии: области не выданы", Granted: false})
		return sid, "Память Сёмы: этому агенту в этой папке области ещё не выданы — выдать можно на экране «Память».", r
	}
	query := hint
	if query == "" {
		query = shortPath(path) + " " + strings.Join(o.Consumer.Areas, " ")
	}
	r = e.Recall(ctx, o, model, query)
	// кристалл прошлой сессии — всегда, даже если поиск его не поднял
	saved, note := 0, "начало сессии · экономия не считается"
	if last := e.lastCrystal(o.Consumer.ID); last != nil {
		if !containsMemory(r.Items, last.ID) {
			r.Items = append([]*store.Memory{last}, r.Items...)
			r.Text = e.memoryBlock(r)
			r.Tokens = tokensOf(r.Text)
		}
		if prevSummary != "" {
			saved, note = tokensOf(prevSummary), "начало сессии · вместо перечитывания сводки прошлой сессии"
		}
	}
	e.LogRecall(o, r, saved, note)
	if r.Text == "" && len(r.Closed) > 0 {
		return sid, "Память Сёмы: области " + strings.Join(r.Closed, ", ") + " закрыты для облачной модели — открыть можно тумблером «наружу» на экране «Память».", r
	}
	return sid, r.Text, r
}

func containsMemory(list []*store.Memory, id int64) bool {
	for _, m := range list {
		if m.ID == id {
			return true
		}
	}
	return false
}

// lastCrystal — кристалл последней завершённой сессии потребителя.
func (e *Engine) lastCrystal(consumerID int64) *store.Memory {
	s := e.store.LastCrystalSession(consumerID)
	if s == nil {
		return nil
	}
	m, err := e.store.MemoryByID(s.Crystal)
	if err != nil || m.Status != store.MemOK {
		return nil
	}
	return m
}

func sessionTime(rfc string) time.Time {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return time.Time{}
	}
	return t
}

// SessionNote дописывает в открытую сессию кусок разговора: хук Stop
// присылает последний обмен репликами, сводка копится, кристалл делается
// при закрытии.
func (e *Engine) SessionNote(agent, path, text string) error {
	o := e.ExternalOrigin(agent, path)
	if len(o.Consumer.Areas) == 0 {
		return fmt.Errorf("заметка не сохранена: области памяти не выданы")
	}
	text = dropSecretLines(strings.TrimSpace(text), e)
	if text == "" {
		return nil
	}
	open := e.store.OpenMemorySession(o.Consumer.ID)
	if open == nil {
		sid, err := e.store.StartMemorySession(o.Consumer.ID, o.Agent, "")
		if err != nil {
			return err
		}
		open = &store.MemorySession{ID: sid}
	}
	return e.noteAndFold(open.ID, text)
}

// MergeNestedConsumers сливает потребителей одного агента из подпапок в
// проект: сессии переезжают, права остаются у корня.
func (e *Engine) MergeNestedConsumers() {
	list, err := e.store.MemoryConsumers()
	if err != nil {
		return
	}
	for _, c := range list {
		if c.Kind != store.ConsumerMCP || c.Path == "" {
			continue
		}
		for _, parent := range list {
			if parent.ID == c.ID || parent.Kind != store.ConsumerMCP || parent.Name != c.Name || parent.Path == "" {
				continue
			}
			if strings.HasPrefix(c.Path, strings.TrimRight(parent.Path, "/")+"/") {
				_ = e.store.MergeMemoryConsumers(c.ID, parent.ID)
				break
			}
		}
	}
}

// CrystallizeSession делает кристалл из одной сессии по запросу с экрана.
func (e *Engine) CrystallizeSession(ctx context.Context, id int64) error {
	s, err := e.store.MemorySession(id)
	if err != nil {
		return err
	}
	if s.Ended == "" {
		_ = e.store.EndMemorySession(id, s.Summary)
	}
	if strings.TrimSpace(s.Summary) == "" {
		return fmt.Errorf("у сессии пустая сводка — сводить нечего")
	}
	if !e.MemoryModelSet() {
		return fmt.Errorf("роль «Память» без модели")
	}
	_ = e.store.SetSessionCrystal(id, 0)
	if n := e.crystallize(ctx); n == 0 {
		return fmt.Errorf("модель не вернула кристалл — подробности в логе сервера")
	}
	return nil
}

// SessionEnd закрывает сессию сводкой: кристалл и факты из неё сделает
// сведение ролью «Память». Наблюдения проходят guardrails сразу.
func (e *Engine) SessionEnd(ctx context.Context, agent, path string, sessionID int64, summary string, observations []string) error {
	o := e.ExternalOrigin(agent, path)
	if sessionID == 0 {
		if last := e.store.LastMemorySession(o.Consumer.ID); last != nil && last.Ended == "" {
			sessionID = last.ID
		} else {
			sid, err := e.store.StartMemorySession(o.Consumer.ID, o.Agent, "")
			if err != nil {
				return err
			}
			sessionID = sid
		}
	}
	session, err := e.store.MemorySession(sessionID)
	if err != nil || session.ConsumerID != o.Consumer.ID {
		return fmt.Errorf("сессия памяти недоступна этому потребителю")
	}
	o.SessionID = sessionID
	summary = strings.TrimSpace(summary)
	if summary == "" { // хук конца сессии без сводки: берём накопленное заметками
		summary = session.Summary
	}
	if mask := e.secretMask(summary); mask != "" {
		e.store.LogMemory(store.MemoryLogEntry{Consumer: o.Consumer.Label(), SessionID: sessionID, Action: "reject",
			Detail: "сводка сессии: маска секрета " + mask + " — строки с секретом убраны", Granted: false})
		summary = dropSecretLines(summary, e)
	}
	if len(o.Consumer.Areas) == 0 {
		e.store.LogMemory(store.MemoryLogEntry{Consumer: o.Consumer.Label(), SessionID: sessionID, Action: "deny",
			Detail: "конец сессии: области не выданы, сводка не сохранена", Granted: false})
		return e.store.EndMemorySession(sessionID, "")
	}
	area := o.Consumer.Areas[0]
	for _, obs := range observations {
		_, _ = e.Remember(ctx, o, MemoryRecord{Kind: store.MemObservation, Area: area, Text: obs})
	}
	if err := e.store.EndMemorySession(sessionID, clip(summary, 8000)); err != nil {
		return err
	}
	e.store.LogMemory(store.MemoryLogEntry{Consumer: o.Consumer.Label(), SessionID: sessionID, Area: area,
		Action: "observe", Detail: fmt.Sprintf("конец сессии: сводка %d символов, наблюдений %d · кристалл сделает сведение", len([]rune(summary)), len(observations)),
		Granted: true, Tokens: tokensOf(summary)})
	// сводку сессии внешнего агента не откладываем до часа: она свежая,
	// а следующая сессия может начаться через минуту
	if e.MemoryModelSet() {
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			e.crystallize(cctx)
		}()
	}
	return nil
}

// dropSecretLines убирает из текста строки, где сработала маска.
func dropSecretLines(text string, e *Engine) string {
	var keep []string
	for _, line := range strings.Split(text, "\n") {
		if e.secretMask(line) == "" {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

// ExternalRecall / ExternalRemember — инструменты MCP.
func (e *Engine) ExternalRecall(ctx context.Context, agent, path, model, query string) Recalled {
	model = externalModel(model)
	o := e.ExternalOrigin(agent, path)
	if last := e.store.LastMemorySession(o.Consumer.ID); last != nil && last.Ended == "" {
		o.SessionID = last.ID
	}
	r := e.Recall(ctx, o, model, query)
	e.LogRecall(o, r, 0, "экономия не считается")
	return r
}

func (e *Engine) ExternalRemember(ctx context.Context, agent, path, area, kind, text, file, symbol, source string) (*store.Memory, error) {
	o := e.ExternalOrigin(agent, path)
	if last := e.store.LastMemorySession(o.Consumer.ID); last != nil && last.Ended == "" {
		o.SessionID = last.ID
	}
	if kind == "" {
		kind = store.MemFact
	}
	if area == "" && len(o.Consumer.Areas) > 0 {
		area = o.Consumer.Areas[0]
	}
	rec := MemoryRecord{Kind: kind, Area: area, Text: text}
	// урок про место в коде возвращается тому, кто придёт это место
	// править: ради этого и заводилась координата
	if file != "" || symbol != "" {
		// источник записи: человеку важно, кто это утверждает — урок из
		// работы и требование спеки весят по-разному
		src := source
		if src == "" {
			src = "агент «" + agent + "»"
		}
		rec.Place = &store.Place{Repo: repoByPath(path), File: file, Symbol: symbol, Source: src}
	}
	return e.Remember(ctx, o, rec)
}

// externalModel — внешний агент без названной модели считается облаком:
// закрытые области ему не уходят.
func externalModel(model string) string {
	if strings.TrimSpace(model) == "" {
		return "облако"
	}
	return model
}
