package api

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Экран «Очередь»: что ждёт, что идёт, что застряло, с какой скоростью и
// когда кончится. Пределы правятся здесь же: на Raspberry Pi важнее не
// сделать быстро, а не занять весь процессор.

const queueScreen = "queue"

func (s *Server) buildQueue(doc *Document) error {
	st := s.eng.Store()
	doc.Heading = "Очередь"
	doc.Subheading = "фоновая работа Сёмы: что ждёт, что идёт, что застряло"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	stats, err := st.WorkStats()
	if err != nil {
		return err
	}
	waiting, held, running := 0, 0, 0
	for _, st := range stats {
		waiting += st.Waiting + st.Paused
		held += st.Held
		running += st.Running
	}
	paused := s.eng.QueuePaused()
	title := fmt.Sprintf("Сейчас · ждут %d · в работе %d · застряли %d", waiting, running, held)
	if paused {
		title += " · на паузе"
	}
	doc.ToolbarActions = []Action{{Action: "toggle_queue_pause",
		Label: map[bool]string{true: "Продолжить", false: "Пауза"}[paused]}}

	// состояние очереди разом, без чтения строк
	add(Component{Type: "cards", Children: []Component{
		{Type: "metric", Title: "Ждут", Text: strconv.Itoa(waiting), Caption: "в очереди"},
		{Type: "metric", Title: "В работе", Text: strconv.Itoa(running), Caption: "прямо сейчас"},
		{Type: "metric", Title: "Застряли", Text: strconv.Itoa(held), Caption: "после всех попыток"},
		{Type: "metric", Title: "Видов работ", Text: strconv.Itoa(len(stats)), Caption: "всего в очереди"},
	}})
	// тумблер вместо кнопки: «Пауза» описывала действие, а состояние
	// приходилось искать в заголовке
	queueOn := "on"
	if paused {
		queueOn = "off"
	}
	add(Component{Type: "row", Layout: "wide", ID: "queue-run", Subtitle: "Очередь работает",
		Caption: map[bool]string{true: "на паузе — работы стоят и ждут вас", false: "работы разбираются по одной с паузой"}[paused],
		Toggle:  queueOn,
		Actions: []Action{{Action: "toggle_queue_pause", Label: "toggle"}}})

	var rows []Component
	for _, w := range stats {
		if w.Waiting+w.Running+w.Held+w.Paused == 0 && w.DoneHr == 0 {
			continue
		}
		name := engine.QueueKinds[w.Kind]
		if name == "" {
			name = w.Kind
		}
		badge := Badge{Text: "пусто", Color: "#8E8E93"}
		dot := ""
		switch {
		case w.Held > 0:
			badge, dot = Badge{Text: fmt.Sprintf("застряли %d", w.Held), Color: "#C4271C"}, "#FF3B30"
		case w.Running > 0:
			badge, dot = Badge{Text: "идёт", Color: "#0063CC"}, "#0058B0"
		case w.Paused > 0:
			badge, dot = Badge{Text: "ждёт времени", Color: "#B25000"}, "#FF9500"
		case w.Waiting > 0:
			badge, dot = Badge{Text: fmt.Sprintf("ждут %d", w.Waiting), Color: "#0063CC"}, "#0058B0"
		}
		caption := fmt.Sprintf("ждут %d · сделано за час %d", w.Waiting+w.Paused, w.DoneHr)
		if w.DoneHr > 0 && w.Waiting+w.Paused > 0 {
			hours := float64(w.Waiting+w.Paused) / float64(w.DoneHr)
			caption += " · " + humanLeft(hours)
		}
		var props []Prop
		if w.Held > 0 {
			list, _ := st.WorkList(w.Kind, store.WorkHeld, 5)
			for _, item := range list {
				props = append(props, Prop{Label: "объект " + item.Ref, Value: fmt.Sprintf("%d попыток · %s", item.Attempts, clipRunes(item.LastErr, 120)), Color: "#FF3B30"})
			}
		}
		if w.Paused > 0 {
			list, _ := st.WorkList(w.Kind, store.WorkWaiting, 5)
			for _, item := range list {
				if item.NotBefore == "" || item.LastErr == "" {
					continue
				}
				props = append(props, Prop{Label: "пауза до " + shortTime(item.NotBefore), Value: clipRunes(item.LastErr, 120), Color: "#FF9500"})
			}
		}
		if p := s.eng.QueuePauseFor(w.Kind); p > 0 {
			caption += fmt.Sprintf(" · свой предел: пауза %d мс", p)
		}
		actions := []Action{
			{Action: "queue_bump", Target: w.Kind, Label: "Ускорить"},
			{Action: "queue_pause_kind", Target: w.Kind, Label: "Предел скорости",
				Prompt: "Пауза между работами этого вида в миллисекундах; 0 — как у всех",
				Value:  strconv.Itoa(s.eng.QueuePauseFor(w.Kind))},
		}
		if w.Held > 0 {
			actions = append(actions,
				Action{Action: "queue_retry", Target: w.Kind, Label: "Повторить застрявшие", Style: "primary"},
				Action{Action: "queue_drop", Target: w.Kind, Label: "Пропустить", Style: "destructive",
					Confirm: "Пропустить застрявшие работы вида «" + name + "»? Они больше не будут сделаны.", ConfirmOK: "Пропустить"})
		}
		row := Component{Type: "row", Layout: "wide", ID: "queue-" + w.Kind, Level: dot, Dot: levelDot(dot), Subtitle: name,
			Caption: caption, Badges: []Badge{badge}, Actions: actions, Props: props}
		if len(props) > 0 {
			row.Expandable = true
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = append(rows, emptyState("Работы нет",
			"Всё сделано. Новая появится сама, когда будет что разбирать: письмо, файл или запись памяти", "tray"))
	}
	add(Component{Type: "section", Title: title, Children: rows})
	help := "локальная модель справляется сама"
	if !s.eng.CloudHelpOn() {
		help = "помощь облачной выключена: работа ждёт локальную"
	} else if note := s.eng.Store().Setting("queue.cloud_note", ""); note != "" {
		help = "облачная запасная подключалась: " + note
	}
	add(Component{Type: "text", Style: "secondary", Text: "Помощь моделей: " + help +
		". Облачная запасная зовётся, только когда очередь длиннее порога или локальная на передышке."})
	add(Component{Type: "text", Style: "secondary",
		Text: "Одна работа на объект: повторная постановка поднимает приоритет, а не создаёт вторую. " +
			"Недоступность модели и исчерпанный бюджет не тратят попытки — работа ждёт с отметкой «не раньше». " +
			"Ничего не уходит из очереди без успеха или вашего решения."})

	cloudAfter := s.eng.CloudHelpAfter()
	if !s.eng.CloudHelpOn() {
		cloudAfter = 0
	}
	load := "нет данных (эта машина не Linux)"
	if v, ok := engine.LoadPerCPU(); ok {
		load = fmt.Sprintf("%.2f на ядро сейчас", v)
	}
	add(Component{Type: "form", ID: "queue-limits", Title: "Пределы", Collapsed: true,
		Fields: []Field{
			{Type: "stepper", Name: "batch", Label: "Работ за проход", Value: strconv.Itoa(s.eng.QueueBatch()), Min: 1, Max: 100, Step: 1},
			{Type: "stepper", Name: "pause", Label: "Пауза между работами, мс", Hint: "чтобы не занимать процессор целиком: на Raspberry Pi это главное",
				Value: strconv.Itoa(s.eng.QueuePauseMs()), Min: 0, Max: 10000, Step: 100},
			{Type: "stepper", Name: "pause_local", Label: "Пауза, когда думает локальная модель, мс", Hint: "локальная модель занимает процессор целиком: между работами даём машине выдохнуть",
				Value: strconv.Itoa(s.eng.QueuePauseLocalMs()), Min: 0, Max: 60000, Step: 500},
			{Type: "text", Name: "load", Label: "Пропускать проход при нагрузке выше", Hint: "средняя нагрузка на ядро · " + load + " · 0 — не смотреть",
				Value: fmt.Sprintf("%.1f", s.eng.QueueMaxLoad())},
			{Type: "stepper", Name: "cloud_after", Label: "Звать облачную, когда ждут больше", Hint: "локальная не успевает — помогает запасная модель роли; 0 отключает помощь",
				Value: strconv.Itoa(cloudAfter), Min: 0, Max: 1000, Step: 5},
			{Type: "stepper", Name: "tries", Label: "Попыток до «застряло»", Value: strconv.Itoa(s.eng.QueueMaxTries()), Min: 1, Max: 20, Step: 1},
			{Type: "stepper", Name: "keep", Label: "Хранить сделанное, дней", Value: strconv.Itoa(s.eng.QueueKeepDays()), Min: 1, Max: 90, Step: 1},
		},
		Actions: []Action{{Action: "set_queue_limits", Label: "Сохранить", Style: "primary"}}})
	return nil
}

func humanLeft(hours float64) string {
	switch {
	case hours < 0.05:
		return "почти всё"
	case hours < 1:
		return fmt.Sprintf("~%d мин", int(hours*60))
	case hours < 24:
		return fmt.Sprintf("~%.0f ч", hours)
	default:
		return fmt.Sprintf("~%.0f дн", hours/24)
	}
}

func shortTime(rfc string) string {
	if t, err := time.Parse(time.RFC3339, rfc); err == nil {
		return t.Format("15:04")
	}
	return rfc
}

// queueBlock — сводка очереди для «Здоровья».
func (s *Server) queueBlock() *Component {
	stats, err := s.eng.Store().WorkStats()
	if err != nil {
		return nil
	}
	waiting, held := 0, 0
	var busiest string
	for _, w := range stats {
		waiting += w.Waiting + w.Paused
		held += w.Held
		if w.Waiting > 0 && busiest == "" {
			busiest = engine.QueueKinds[w.Kind]
		}
	}
	if waiting == 0 && held == 0 {
		return nil
	}
	dot := LevelInfo
	if held > 0 {
		dot = LevelAlert
	}
	caption := "фоновая работа идёт по одной, с паузой между"
	if busiest != "" {
		caption = strings.ToLower(busiest) + " в работе · " + caption
	}
	return &Component{Type: "section", Title: "Очередь", Children: []Component{{
		Type: "row", Layout: "wide", Level: dot, Dot: levelDot(dot),
		Subtitle: fmt.Sprintf("Ждут %d · застряли %d", waiting, held), Caption: caption,
		Actions: []Action{{Action: "$goto", Target: queueScreen, Label: "Открыть"}}}}}
}

func (s *Server) queueActions(req actionRequest) (string, error) {
	st := s.eng.Store()
	switch req.Action {
	case "toggle_queue_pause":
		return queueScreen, s.eng.SetQueuePaused(!s.eng.QueuePaused())
	case "queue_bump":
		return queueScreen, st.BumpWork(req.Target)
	case "queue_pause_kind":
		ms, err := strconv.Atoi(strings.TrimSpace(req.Params["value"]))
		if err != nil || ms < 0 {
			return "", fmt.Errorf("нужно число миллисекунд, 0 — как у всех")
		}
		return queueScreen, s.eng.SetQueuePauseFor(req.Target, ms)
	case "queue_retry":
		n, err := st.RetryHeld(req.Target)
		if err != nil {
			return "", err
		}
		s.eng.CloseNotification("queue:"+req.Target, store.NotifyDone, fmt.Sprintf("повторяем %d", n))
		return queueScreen, nil
	case "queue_drop":
		n, err := st.DropWork(req.Target)
		if err != nil {
			return "", err
		}
		s.eng.CloseNotification("queue:"+req.Target, store.NotifyDismissed, fmt.Sprintf("пропущено %d", n))
		return queueScreen, nil
	case "set_queue_limits":
		set := func(name, key string, min int) {
			if v, err := strconv.Atoi(strings.TrimSpace(req.Params[name])); err == nil && v >= min {
				_ = st.SetSetting(key, strconv.Itoa(v))
			}
		}
		set("batch", "queue.batch", 1)
		set("pause", "queue.pause_ms", 0)
		set("pause_local", "queue.pause_local_ms", 0)
		if v, err := strconv.Atoi(strings.TrimSpace(req.Params["cloud_after"])); err == nil {
			if v <= 0 {
				_ = s.eng.SetCloudHelp(false)
			} else {
				_ = s.eng.SetCloudHelp(true)
				_ = st.SetSetting("queue.cloud_after", strconv.Itoa(v))
			}
		}
		set("tries", "queue.max_tries", 1)
		set("keep", "queue.keep_days", 1)
		if v := strings.TrimSpace(req.Params["load"]); v != "" {
			_ = st.SetSetting("queue.max_load", strings.ReplaceAll(v, ",", "."))
		}
		return queueScreen, nil
	}
	return "", errUnhandled
}
