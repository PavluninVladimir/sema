package api

// Экран одного агента: `settings:agents@<имя>~вкладка`. Шапку (путь назад,
// вкладки, закреплённая строка) рисует общий entityFrame, содержимое — здесь:
//
//	Обзор        — кто это: роль, отпечаток сертификата, когда выдан и когда
//	               истекает, последняя связь, экономия, память; «Опасное» внизу
//	Способности  — объявленные агентом, у каждой тумблер «разрешить»: новая
//	               появляется выключенной, неразрешённую Сёма не зовёт вовсе
//	Журнал       — что шло через этого агента: чипы отбора, двадцать строк,
//	               «Показать ещё»
//
// Решений экран не принимает: способность разрешает человек поимённо, отзыв
// действует немедленно, сменившийся отпечаток принимается только его
// подтверждением. Он зовёт прежние действия (`allow_agent`, `revoke_agent`,
// `accept_agent_fp`, `forget_agent`, `check_agent`), «Опасное» — внизу своей
// карточкой, у каждой кнопки `Confirm` и `ConfirmOK`. Тумблера «разрешить все»
// нет и не будет: права пачкой не расширяются.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/ca"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// agentTabs — вкладки агента. Счётчик у вкладки говорит, что там есть решать,
// до того как человек туда зайдёт.
func (s *Server) agentTabs(a *store.Agent) []innerTab {
	tabs := []innerTab{
		{ID: agentsTabList, Name: "Обзор"},
		{ID: agentTabCaps, Name: "Способности"},
		{ID: agentsTabLog, Name: "Журнал"},
	}
	if a.State == store.AgentFPBad {
		tabs[0].Count, tabs[0].Level = "!", LevelAlert
	}
	if _, declared, waiting := agentCaps(a); declared > 0 && a.State == store.AgentOK {
		if len(waiting) > 0 {
			tabs[1].Count, tabs[1].Level = strconv.Itoa(len(waiting))+" не разрешены", LevelInfo
		}
	}
	if n := s.eng.Store().CountAgentLog(a.Name, store.AgentLogFail); n > 0 {
		tabs[2].Count, tabs[2].Level = plural(n, "неудача", "неудачи", "неудач"), LevelWarn
	}
	return tabs
}

func (s *Server) buildAgent(doc *Document, addr agentsAddr, filter string, limit int) error {
	st := s.eng.Store()
	doc.Layout = "read"
	a, ok := st.Agent(addr.Name)
	if !ok {
		// агента могли забыть, пока экран был открыт: не ошибка, а объяснение
		doc.Heading = "Агенты"
		doc.Components = append(doc.Components,
			Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: agentsScreen, Label: "‹ Агенты"}}},
			emptyState("Агента «"+addr.Name+"» нет", "Его забыли или переименовали. Список агентов — по кнопке «‹ Агенты» выше", "person"))
		return nil
	}
	links := s.agentLinksFor(doc)
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	switch addr.Tab {
	case agentTabCaps:
		s.agentCapabilities(add, a)
	case agentsTabLog:
		if err := s.agentJournal(doc, a.Name, filter, limit); err != nil {
			return err
		}
	default:
		addr.Tab = agentsTabList
		s.agentOverview(add, a, links)
	}

	letter, color := agentTile(a)
	e := entity{Section: agentsScreen, Back: "Агенты", ID: a.Name, Name: a.Name,
		Avatar: letter, AvatarColor: color, Caption: agentCaption(a),
		Tab: addr.Tab, Tabs: s.agentTabs(a), Switch: "Агент"}
	e.Badges = []Badge{agentStatePill(a, links)}
	if a.State == store.AgentFPBad {
		e.Level = LevelAlert
	}
	e.Primary = s.agentPrimary(a)
	// «···»: что не главное — пути по вкладкам (у телефона вкладки в одну
	// полосу, а меню под рукой) и новое приглашение, когда главная кнопка другая
	e.Menu = []Action{
		{Action: "$goto", Target: entityAddr(agentsScreen, a.Name, agentTabCaps), Label: "Способности"},
		{Action: "$goto", Target: entityAddr(agentsScreen, a.Name, agentsTabLog), Label: "Журнал"},
	}
	if e.Primary == nil || e.Primary.Action != "invite_agent" {
		e.Menu = append(e.Menu, Action{Action: "invite_agent", Target: a.Name, Label: "Новое приглашение"})
	}
	if all, err := st.Agents(); err == nil {
		e.Others = sortedNames(all)
	}
	entityFrame(doc, e)
	return nil
}

// certDate — дата сертификата словами: «14 октября 2027».
func certDate(t time.Time) string {
	t = t.Local()
	return fmt.Sprintf("%d %s %d", t.Day(), monthNames[t.Month()-1], t.Year())
}

// agentOverview — «Обзор»: кто это. Свойства — строки, опасное — внизу.
func (s *Server) agentOverview(add func(Component), a *store.Agent, links map[string]string) {
	role := engine.AgentRole(a)

	// ---- то, что ждёт именно этого агента, — вверху ----
	var alerts []Component
	switch a.State {
	case store.AgentFPBad:
		alerts = append(alerts, Component{Type: "row", Layout: "wide", ID: "agent-fp-changed", Level: LevelAlert, Dot: levelDot(LevelAlert),
			Subtitle: "Отпечаток сертификата сменился",
			Caption: "Соединения отклоняются, пока вы не решите. Так бывает после переустановки агента; " +
				"если вы его не переустанавливали — это тревога: звонить может другая машина",
			Facts: []Prop{{Label: "Было", Value: a.Fingerprint}, {Label: "Стало", Value: a.NewFP, Color: "#C4271C"}}})
	case store.AgentRevoked:
		alerts = append(alerts, Component{Type: "row", Layout: "wide", ID: "agent-revoked", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "Доступ отозван",
			Caption:  "Сертификат остался у агента на руках, но Сёма его не признаёт, а способности сняты. Вернуть доступ — только новым приглашением"})
	}
	s.mu.Lock()
	check := s.lastAgentCheck
	s.mu.Unlock()
	if check.Name == a.Name && check.Text != "" {
		alerts = append(alerts, Component{Type: "row", Layout: "wide", ID: "agent-check", Subtitle: "Проверка связи", Caption: check.Text})
	}
	if len(alerts) > 0 {
		add(Component{Type: "section", ID: "agent-alerts", Children: alerts})
	}

	// ---- свойства ----
	_, live := links[a.Name]
	connection := "не на связи"
	if since, ok := links[a.Name]; ok {
		connection = "на связи " + since
	}
	rows := []Component{
		{Type: "row", Layout: "wide", ID: "agent-role", Subtitle: "Роль",
			Caption: "«" + role + "» · роли положено: " + allowedForRole(role) + ". Роль закрепляется кодом приглашения, и сменить её можно только новым"},
	}
	if role == engine.RoleGateway {
		// у шлюза направление соединения обратное, и это первое, что человек
		// должен понимать, глядя на него
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-who-calls", Subtitle: "Кто звонит",
			Caption: "Сёма — по адресу шлюза; дома порт не открыт"})
	}
	fp := Component{Type: "row", Layout: "wide", ID: "agent-fingerprint", Subtitle: "Отпечаток сертификата"}
	if a.Fingerprint != "" {
		fp.Caption = a.Fingerprint
		fp.Actions = []Action{{Action: "$copy", Target: a.Fingerprint, Label: "Скопировать"}}
	} else {
		fp.Caption = "ещё не выдан: агент входит одноразовым кодом и получает сертификат"
	}
	rows = append(rows, fp)
	if a.Cert != "" {
		if from, err := ca.Since(a.Cert); err == nil {
			rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-issued", Subtitle: "Сертификат выдан", Caption: certDate(from)})
		}
		if until, err := ca.Until(a.Cert); err == nil {
			row := Component{Type: "row", Layout: "wide", ID: "agent-expires", Subtitle: "Сертификат истекает"}
			left := time.Until(until)
			switch {
			case left < 0:
				row.Caption = certDate(until) + " — срок вышел: агент не сможет позвонить, нужно новое приглашение"
				withLevel(&row, LevelAlert)
			case left < 30*24*time.Hour:
				row.Caption = certDate(until) + " — через " + plural(int(left.Hours()/24), "день", "дня", "дней") + ": позовите агента заново до этого срока"
				withLevel(&row, LevelWarn)
			default:
				row.Caption = certDate(until)
			}
			rows = append(rows, row)
		}
	}
	last := "ещё ни разу не звонил"
	if a.Seen != "" {
		last = whenText(a.Seen)
	}
	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-seen", Subtitle: "Последняя связь", Caption: last + " · сейчас " + connection})
	if a.Note != "" {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-note", Subtitle: "Заметка", Caption: a.Note})
	}
	// Польза в цифрах, а не на веру: сколько строк агент не дал прочитать и
	// чем он умеет отвечать, когда языкового сервера нет
	if live && role == engine.RoleResident && a.State == store.AgentOK {
		if sv, ok := s.eng.AgentSavings(a.Name); ok && sv.Calls > 0 {
			saved := fmt.Sprintf("%s не прочитано за %s (в карточках %d) — примерно %s моделью",
				plural(sv.Saved, "строка", "строки", "строк"), plural(sv.Calls, "ответ", "ответа", "ответов"), sv.CardLines,
				plural(sv.Tokens, "токен", "токена", "токенов"))
			rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-savings", Subtitle: "Сэкономлено", Caption: saved})
			// «за всё время» и «за неделю» — разные вопросы: первое льстит,
			// второе показывает, работает ли это сейчас
			week := s.eng.Store().SavingsSince(a.Name, time.Now().AddDate(0, 0, -7).Format(time.RFC3339))
			switch {
			case week.At == "":
				rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-week", Subtitle: "За неделю", Caption: "снимков ещё нет — счёт начнёт копиться с этого часа"})
			case week.Saved > 0:
				rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-week", Subtitle: "За неделю",
					Caption: plural(week.Saved, "строка", "строки", "строк") + " и " + plural(week.Calls, "ответ", "ответа", "ответов")})
			default:
				rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-week", Subtitle: "За неделю", Caption: "не спрашивали"})
			}
			if sv.Week.Calls > 0 {
				rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-cards", Subtitle: "Карточек за 7 дней",
					Caption: fmt.Sprintf("%d · с приложением из памяти %d", sv.Week.Calls, sv.Week.WithKnowledge)})
			}
			var below []string
			if sv.Ctags {
				below = append(below, "справочник символов")
			}
			if sv.Grep {
				below = append(below, "поиск по тексту")
			}
			if len(below) > 0 {
				rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-fallback", Subtitle: "Без языкового сервера",
					Caption: "отвечает через " + strings.Join(below, " и ")})
			}
		}
	}
	// Память ходит этим же проводом, и когда она замолчит, человек пойдёт
	// смотреть сюда — а не в журнал памяти на другом экране
	if hasCan(a, engine.CanMemory) {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-memory", Subtitle: "Память", Caption: s.eng.AgentMemory(a.Name),
			Actions: []Action{{Action: "$goto", Target: "memory~areas", Label: "Области памяти"}}})
	}
	add(Component{Type: "section", ID: "agent-facts", Children: rows})

	// ---- опасное: внизу своей карточкой, с вопросом ----
	var danger []Component
	if a.State == store.AgentFPBad {
		accept := acceptAgentFPAction(a)
		accept.Label, accept.Style = "Принять…", "destructive"
		danger = append(danger, Component{Type: "row", Layout: "wide", ID: "danger-fingerprint",
			Subtitle: "Принять новый отпечаток",
			Caption:  "Так бывает после переустановки агента; если вы его не переустанавливали — это тревога, и принимать не нужно",
			Actions:  []Action{accept}})
	}
	if a.State != store.AgentRevoked {
		revoke := revokeAgentAction(a)
		revoke.Label = "Отозвать…"
		danger = append(danger, Component{Type: "row", Layout: "wide", ID: "danger-revoke",
			Subtitle: "Отозвать доступ",
			Caption:  "Действует немедленно: Сёма перестаёт признавать сертификат, соединение закрывается, способности снимаются. Вернуть можно только новым приглашением",
			Actions:  []Action{revoke}})
	}
	forget := forgetAgentAction(a)
	forget.Label = "Забыть…"
	danger = append(danger, Component{Type: "row", Layout: "wide", ID: "danger-forget",
		Subtitle: "Забыть агента",
		Caption:  "Он исчезает из справочника, а его сертификат перестаёт признаваться; журнал остаётся",
		Actions:  []Action{forget}})
	add(Component{Type: "section", ID: "agent-danger", Title: "Опасное", Style: "danger", Children: danger})
}

// agentCapabilities — «Способности»: объявляет агент, разрешает человек, и
// каждую отдельно. Новая появляется выключенной, неразрешённую Сёма не зовёт
// вовсе. Права пачкой не расширяются: тумблера «разрешить все» нет.
func (s *Server) agentCapabilities(add func(Component), a *store.Agent) {
	allowed := map[string]bool{}
	for _, c := range a.Allowed {
		allowed[c] = true
	}
	var caps []Component
	for _, c := range a.Can {
		toggle, why := "off", "не разрешена — Сёма её не зовёт вовсе"
		if allowed[c] {
			toggle, why = "on", "разрешена — Сёма может звать её по каналу"
		}
		caps = append(caps, Component{Type: "row", Layout: "wide", ID: "cap-" + a.Name + "-" + c,
			Subtitle: c, Caption: why, Toggle: toggle,
			Actions: []Action{{Action: "allow_agent", Target: a.Name + "~" + c, Label: "toggle"}}})
	}
	if len(caps) == 0 {
		caps = append(caps, emptyState("Агент ещё не объявил способностей",
			"Он объявляет их сам, когда звонит; пока он не звонил или молчит, разрешать нечего", "key"))
	}
	add(Component{Type: "section", ID: "agent-caps", Title: "Что ему разрешено", Children: caps})

	role := engine.AgentRole(a)
	var notes []Component
	notes = append(notes, Component{Type: "row", Layout: "wide", ID: "agent-role-allows", Subtitle: "Роли положено",
		Caption: "«" + role + "»: " + allowedForRole(role) + ". Остального по роли агенту не достанется никогда, сколько бы он ни объявил"})
	if a.State != store.AgentOK {
		notes = append(notes, Component{Type: "row", Layout: "wide", ID: "agent-caps-idle", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "Пока доступ не восстановлен, способности не действуют",
			Caption:  "Даже разрешённые: Сёма не звонит агенту, чьё состояние не «в порядке»"})
	}
	// области памяти выдаются паре «агент разработки + папка» на экране «Память»:
	// здесь только разрешена ли сама способность
	for _, c := range a.Can {
		if c == engine.CanMemory {
			notes = append(notes, Component{Type: "row", Layout: "wide", ID: "agent-memory-areas", Subtitle: "Области памяти",
				Caption: "Способность «память» — только дверь. Какие области видит агент разработки, решается отдельно, для пары «агент + папка»",
				Actions: []Action{{Action: "$goto", Target: "memory~areas", Label: "Области памяти"}}})
			break
		}
	}
	add(Component{Type: "section", ID: "agent-caps-notes", Children: notes})
	add(Component{Type: "section", ID: "agent-caps-how", Title: "Как это устроено", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary",
		Text: "Способности объявляет сам агент, а разрешаете вы — каждую отдельно. Новая появляется выключенной, и неразрешённую Сёма не зовёт вовсе. " +
			"Выключить разрешённую можно в любой момент — Сёма перестаёт её звать сразу. Отзыв доступа целиком — внизу «Обзора»."}}})
}
