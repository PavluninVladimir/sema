package api

// Первая вкладка «Работы Сёмы» — «Самонаблюдение»: что Сёма нашёл в себе.
//
// Наблюдатель, чьи находки негде посмотреть, повторит судьбу журнала MAX:
// сто двадцать одинаковых строк за ночь прошли мимо человека именно потому,
// что лежали только в журнале.
//
// Норма здесь молчит, но не молчит совсем: пустая вкладка называет, что
// проверялось и когда. «Ничего не найдено» и «ничего не проверялось» — не
// одно и то же, и путать их нельзя.
//
// Заголовок, вкладки и остальные вкладки раздела — `settings_work.go`.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const watchScreen = "settings:watch"

// buildSelfWatch — вкладка «Самонаблюдение»; open — сколько находок открыто.
func (s *Server) buildSelfWatch(doc *Document, open int) error {
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	doc.Subheading = watcherCaption(s, open)

	// сам наблюдатель: включён ли и вправе ли останавливать
	s.watcherRows(add)

	findings, err := s.eng.Store().Findings(false, 60)
	if err != nil {
		return err
	}
	var now, past []Component
	for _, f := range findings {
		row := findingRow(f)
		if f.Open {
			now = append(now, row)
		} else {
			past = append(past, row)
		}
	}
	if len(now) > 0 {
		add(Component{Type: "section", ID: "находки-сейчас",
			Title:    "Сейчас · " + plural(len(now), "находка", "находки", "находок"),
			Children: now})
	} else {
		add(Component{Type: "section", ID: "находки-сейчас", Title: "Сейчас",
			Children: []Component{whatChecked(s)}})
	}
	if len(past) > 0 {
		add(Component{Type: "section", ID: "находки-былые", Collapsed: true,
			Title: "Было и прошло · " + strconv.Itoa(len(past)), Children: past})
	}
	return nil
}

// whatChecked — пустое место объясняет себя: что именно проверялось и
// когда. Иначе «пусто» читается как «не работает».
func whatChecked(s *Server) Component {
	c := emptyState("Ничего не найдено",
		"Сёма смотрит за формой собственной работы: повтор без следа, пауза переподключения в потолке, "+
			"счётчик, который должен расти, включённое, которое не происходит, свой итог против содержимого хранилища "+
			"и вопросы самоосмотра — срок сертификата, ключ, целостность базы, место на диске", "checkmark.circle")
	t := s.eng.TaskResult(engineWatchSystem)
	if t == "" {
		c.Caption += " · проходов ещё не было"
		return c
	}
	c.Facts = []Prop{{Label: "Последний проход", Value: t}}
	return c
}

func findingRow(н *store.Finding) Component {
	c := Component{Type: "row", Layout: "wide", ID: "находка-" + strconv.FormatInt(н.ID, 10),
		Subtitle: н.Summary, Caption: н.Numbers,
		Facts: []Prop{
			{Label: "Проверка", Value: н.Kind},
			{Label: "Держится", Value: whenText(н.First.Format("2006-01-02T15:04:05Z07:00"))},
		}}
	if н.Open {
		withLevel(&c, н.Level)
		// у находки всегда есть действие, и оно ведёт прямо туда, где чинят
		if н.Screen != "" {
			c.Actions = []Action{{Action: "$goto", Target: н.Screen, Label: "Разобраться →", Style: "primary"}}
		}
		if н.Measure != store.MeasureAlert {
			c.Badges = []Badge{{Text: н.Measure, Level: LevelWarn}}
		}
	} else {
		c.Style = "secondary"
		if н.Decision != "" {
			c.Facts = append(c.Facts, Prop{Label: "Кончилось", Value: н.Decision})
		}
	}
	return c
}

func watcherCaption(s *Server, open int) string {
	if !s.eng.WatchOn() {
		return "наблюдение выключено — Сёма за формой своей работы не смотрит"
	}
	caption := "Сёма смотрит за формой собственной работы: шесть проверок раз в пять минут"
	if open == 0 {
		return caption + " · сейчас ничего не найдено"
	}
	return caption + " · " + plural(open, "открытая находка", "открытые находки", "открытых находок")
}

// watcherRows — сам наблюдатель: тумблеры и пороги. Опасное здесь
// одно — право останавливать работу, и оно стоит отдельной строкой.
func (s *Server) watcherRows(add func(Component)) {
	var rows []Component
	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "наб-вкл",
		Subtitle: "Смотреть за формой своей работы",
		Caption: "шесть проверок раз в пять минут, только сводные запросы по своей базе; " +
			"к моделям и в сеть наблюдение не ходит",
		Toggle:  boolToggle(s.eng.WatchOn()),
		Actions: []Action{{Action: "toggle_watch", Label: "toggle"}}})

	стоп := Component{Type: "row", Layout: "wide", ID: "наб-стоп",
		Subtitle: "Останавливать работу, которая идёт впустую",
		Toggle:   boolToggle(s.eng.WatchStops()),
		Actions:  []Action{{Action: "toggle_watch_stop", Label: "toggle"}}}
	if s.eng.WatchStops() {
		стоп.Level, стоп.Dot = LevelWarn, levelDot(LevelWarn)
		стоп.Caption = "повтор, который тратит деньги, Сёма останавливает сам: снимает с работы объект, а не весь её вид. " +
			"Работа не теряется — снять паузу можно на «Очереди»"
	} else {
		стоп.Caption = "сейчас только говорит. Дорогая петля стоила 6,5 $ за двое суток; " +
			"право останавливать даётся осознанно, когда видно, на чём оно срабатывает"
	}
	rows = append(rows, стоп)

	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "наб-сверка",
		Subtitle: "Сверять свой итог с хранилищем",
		Caption: "единственная проверка, которая ходит в сеть: раз в час спрашивает Диск, правда ли копии там нет. " +
			"Паузу она не ставит никогда — зависит от чужой доступности",
		Toggle:  boolToggle(s.eng.WatchDisagree()),
		Actions: []Action{{Action: "toggle_watch_disagree", Label: "toggle"}}})

	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "наб-осмотр",
		Subtitle: "Задавать вопросы самоосмотра на ходу",
		Caption: "те же вопросы, что у «-doctor»: срок сертификата, ключ шифрования, целостность базы, место на диске. " +
			"Осмотр запускают раз в месяц, а сертификат кончается на работающей Сёме",
		Toggle:  boolToggle(s.eng.WatchDoctor()),
		Actions: []Action{{Action: "toggle_watch_doctor", Label: "toggle"}}, key: engine.WatchDoctorKey})

	add(Component{Type: "section", ID: "наблюдатель", Title: "Наблюдатель", Children: rows})
	add(Component{Type: "text", Style: "secondary",
		Text: fmt.Sprintf("Пороги: повтор считается петлёй с %s по одному объекту за %s; "+
			"пауза переподключения — состояние, если стоит в потолке дольше получаса. Правятся ниже.",
			plural(s.eng.WatchRepeat(), "работы", "работ", "работ"),
			plural(s.eng.WatchWindow(), "час", "часа", "часов"))})
	add(Component{Type: "form", ID: "наб-пороги", Title: "Пороги", Fields: []Field{
		{Type: "stepper", Name: "repeat", Label: "Повторов по объекту — петля",
			Value: strconv.Itoa(s.eng.WatchRepeat()), Min: 2, Max: 100, Step: 1, Unit: "раз"},
		{Type: "stepper", Name: "window", Label: "За какое окно считать",
			Value: strconv.Itoa(s.eng.WatchWindow()), Min: 1, Max: 72, Step: 1, Unit: "ч"},
		{Type: "stepper", Name: "keep", Label: "Держать закрытые находки",
			Value: strconv.Itoa(s.eng.WatchKeepDays()), Min: 7, Max: 730, Step: 7, Unit: "дн."},
	}, Submit: &Action{Action: "save_watch_limits", Label: "Сохранить"}})
}

const engineWatchSystem = "self_watch"

var _ = strings.TrimSpace
