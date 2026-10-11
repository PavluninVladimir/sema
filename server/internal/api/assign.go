package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Поручения на экранах: карточка в чате (план и ход — по базе, а не тем,
// что лежит в сообщении) и раздел на «Задачах». Нового типа в словаре нет:
// карточка дела со строками шагов и кнопками.

// assignStateBadge — состояние поручения капсулой.
func assignStateBadge(a *store.Assignment) Badge {
	done, total := a.Progress()
	switch a.State {
	case store.AssignDraft:
		return Badge{Text: "ждёт «Начать»", Level: LevelWarn}
	case store.AssignPaused:
		return Badge{Text: "остановлено"}
	case store.AssignWaiting:
		if t, err := time.Parse(time.RFC3339, a.WakeAt); err == nil {
			return Badge{Text: "отложено до " + t.Local().Format("02.01 15:04"), Level: LevelInfo}
		}
		return Badge{Text: "ждёт вас", Level: LevelWarn}
	case store.AssignDone:
		return Badge{Text: "выполнено", Level: LevelOK}
	case store.AssignCancelled:
		return Badge{Text: "отменено"}
	}
	return Badge{Text: fmt.Sprintf("идёт · %d из %d", done, total), Level: LevelInfo}
}

// assignActions — кнопки по состоянию: одна главная и «Отменить» с вопросом.
func assignActions(a *store.Assignment) []Action {
	id := strconv.FormatInt(a.ID, 10)
	if !a.Open() {
		// закрыли по ошибке (модель отметила последний шаг не по делу) —
		// возвращается в работу одной кнопкой
		return []Action{{Action: "assign_reopen", Target: id, Label: "Вернуть в работу"}}
	}
	var out []Action
	switch a.State {
	case store.AssignDraft:
		out = append(out, Action{Action: "assign_start", Target: id, Label: "Начать", Style: "primary"})
	case store.AssignActive:
		out = append(out, Action{Action: "assign_pause", Target: id, Label: "Остановить"})
	case store.AssignWaiting:
		// ждёт человека — но и остановить его можно: заглушки секретов,
		// заведённые заранее, иначе вернули бы поручение в работу сами
		out = append(out, Action{Action: "assign_resume", Target: id, Label: "Продолжить", Style: "primary"},
			Action{Action: "assign_pause", Target: id, Label: "Остановить"})
	default:
		out = append(out, Action{Action: "assign_resume", Target: id, Label: "Продолжить", Style: "primary"})
	}
	return append(out, Action{Action: "assign_cancel", Target: id, Label: "Отменить…", Style: "destructive",
		Confirm:   "Отменить поручение «" + a.Goal + "»? Ходов по нему больше не будет, сделанное останется в переписке.",
		ConfirmOK: "Отменить поручение"})
}

func (s *Server) assignCardComponent(card engine.ToolCard, at string) Component {
	st := s.eng.Store()
	a, ok := st.Assignment(card.Assign.ID)
	if !ok {
		return Component{Type: "toolcard", Icon: "list.bullet", Title: "Поручение", Meta: "Сёма · " + at,
			Caption: "Поручения больше нет."}
	}
	switch card.Assign.Kind {
	case "secret":
		c := Component{Type: "toolcard", Icon: "key", Title: "Нужен секрет", Meta: "Поручение «" + a.Goal + "» · " + at,
			Caption: "Пароль в чат не присылайте: заведите секрет — поручение продолжится само."}
		missing := 0
		for _, name := range card.Assign.Secrets {
			if _, ok := st.Secret(name); ok {
				continue
			}
			missing++
			c.Actions = append(c.Actions, Action{Action: "$goto", Target: engine.SecretAddScreen(name),
				Label: "Завести «" + name + "»", Style: "primary"})
		}
		c.Text = "Модуль просит: " + strings.Join(card.Assign.Secrets, ", ")
		if missing == 0 {
			c.Badges = []Badge{{Text: "заведены", Level: LevelOK}}
			withLevel(&c, LevelOK)
		} else {
			c.Badges = []Badge{{Text: "ждёт вас", Level: LevelWarn}}
			withLevel(&c, LevelWarn)
		}
		return c
	case "done":
		c := Component{Type: "toolcard", Icon: "checkmark.seal.fill", Title: card.Title, Meta: "Сёма · " + at,
			Text: firstText(card.Result, a.Summary), Badges: []Badge{assignStateBadge(a)}, Actions: assignActions(a)}
		if a.State == store.AssignDone {
			withLevel(&c, LevelOK)
		}
		return c
	}
	c := Component{Type: "toolcard", Icon: "list.bullet", Title: "Поручение · " + a.Goal, Meta: "Сёма · " + at,
		Badges: []Badge{assignStateBadge(a)}, Actions: assignActions(a)}
	cur := a.Current()
	for _, step := range a.Steps {
		row := Component{Type: "row", Subtitle: fmt.Sprintf("%d. %s", step.N, step.Text), Caption: step.Result}
		switch {
		case step.State == store.StepDone:
			row.Badges = []Badge{{Text: "сделано", Level: LevelOK}}
		case step.State == store.StepSkipped:
			row.Badges = []Badge{{Text: "пропущен"}}
		case cur != nil && step.N == cur.N && a.State == store.AssignActive:
			row.Badges = []Badge{{Text: "сейчас", Level: LevelInfo}}
		}
		c.Children = append(c.Children, row)
	}
	switch a.State {
	case store.AssignWaiting:
		c.Caption = "Ждёт вашего ответа: " + a.Question
		withLevel(&c, LevelWarn)
	case store.AssignDraft:
		c.Caption = "Сёма пойдёт по шагам сам, когда нажмёте «Начать»."
	case store.AssignActive:
		c.Caption = "Сёма идёт по шагам сам; изменения по-прежнему ждут вашей кнопки."
	case store.AssignDone:
		c.Caption = "Итог: " + a.Summary
		withLevel(&c, LevelOK)
	}
	return c
}

func firstText(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// assignRows — раздел «Поручения» на «Задачах»: незаконченные и десять
// последних закрытых. Пусто — раздела нет.
func (s *Server) assignSection() (Component, bool) {
	st := s.eng.Store()
	open, _ := st.Assignments(0, store.AssignDraft, store.AssignActive, store.AssignPaused, store.AssignWaiting)
	closed, _ := st.Assignments(10, store.AssignDone, store.AssignCancelled)
	all := append(open, closed...)
	if len(all) == 0 {
		return Component{}, false
	}
	var rows []Component
	for i := range all {
		a := &all[i]
		done, total := a.Progress()
		caption := fmt.Sprintf("шаг %d из %d", done, total)
		if cur := a.Current(); cur != nil && a.Open() {
			caption += " · " + cur.Text
		}
		switch {
		case a.State == store.AssignWaiting:
			caption = "ждёт вас: " + a.Question
		case !a.Open() && a.Summary != "":
			caption = a.Summary
		}
		row := Component{Type: "row", ID: "assign-" + strconv.FormatInt(a.ID, 10), Icon: "list.bullet",
			Subtitle: a.Goal, Caption: clipRunes(caption, 160), Badges: []Badge{assignStateBadge(a)},
			Actions: append([]Action{{Action: "$goto", Target: "chat:" + strconv.FormatInt(a.Chat, 10), Label: "Открыть чат"}},
				assignActions(a)...)}
		if a.State == store.AssignWaiting {
			withLevel(&row, LevelWarn)
		}
		rows = append(rows, row)
	}
	return Component{Type: "section", ID: "assignments", Title: fmt.Sprintf("Поручения · %d идут", len(open)), Children: rows}, true
}

// assignActionsGroup — «Начать», «Остановить», «Продолжить», «Отменить».
func (s *Server) assignActionsGroup(req actionRequest) (string, error) {
	acts := map[string]string{"assign_start": "start", "assign_pause": "pause", "assign_resume": "resume", "assign_cancel": "cancel",
		"assign_reopen": "reopen"}
	act, ok := acts[req.Action]
	if !ok {
		return "", errUnhandled
	}
	id, err := req.one()
	if err != nil {
		return "", err
	}
	if _, err := s.eng.AssignmentAction(context.Background(), id, act); err != nil {
		return "", err
	}
	return "", nil
}
