package api

import (
	"fmt"
	"strconv"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Экран «Развитие»: чему Сёма научился сам. Ничего не попадает в работу без
// согласия человека — черновик лежит здесь, пока не нажмут.

const learningScreen = "settings:learning"

func (s *Server) buildLearning(doc *Document) error {
	doc.Heading = "Развитие"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	eng, st := s.eng, s.eng.Store()
	week := time.Now().AddDate(0, 0, -7)
	stat := eng.LearnStats(week)

	add(Component{Type: "text", Style: "secondary",
		Text: "Из законченной работы Сёма пишет себе скилл, из сорванной — правку к нему. " +
			"Черновик ждёт вашего решения: до согласия в промпт ничего не попадает. " +
			"Отклонённое он больше не предложит."})

	add(Component{Type: "cards", Children: []Component{
		{Type: "metric", ID: "learn-open", Title: "Ждут решения", Text: strconv.Itoa(stat.Open), Caption: "черновиков"},
		{Type: "metric", ID: "learn-accepted", Title: "Принято за неделю", Text: strconv.Itoa(stat.Accepted), Caption: "стало скиллами"},
		{Type: "metric", ID: "learn-rejected", Title: "Отклонено", Text: strconv.Itoa(stat.Rejected), Caption: "такое больше не предложит"},
		{Type: "metric", ID: "learn-lessons", Title: "Уроков за неделю", Text: strconv.Itoa(stat.Lessons), Caption: "из закрытых вопросов"},
	}})

	// черновики на решение
	open, err := st.SkillDrafts(store.DraftOpen, 20)
	if err != nil {
		return err
	}
	var rows []Component
	for _, d := range open {
		rows = append(rows, draftRow(d))
	}
	if len(rows) == 0 {
		rows = append(rows, Component{Type: "row", Layout: "wide", Subtitle: "Черновиков нет",
			Caption: "появятся после непростой работы, законченной удачно"})
	}
	add(Component{Type: "section", Title: "Ждут вашего решения", Children: rows})

	// польза скиллов; предотвращённое считается отдельно от исходов: работа
	// может закончиться удачей и всё равно содержать момент, где скилл не дал
	// сделать глупость
	saved := s.eng.Store().SkillSaved(time.Now().AddDate(0, 0, -7).Format(time.RFC3339))
	scores, err := st.SkillScores(week.Format(time.RFC3339))
	if err != nil {
		return err
	}
	var useful []Component
	for _, sc := range scores {
		k, ok := st.Skill(sc.Name)
		if !ok {
			continue
		}
		dot, verdict := LevelOK, "помогает"
		switch {
		case sc.Wins == 0 && sc.Used >= 5:
			dot, verdict = LevelAlert, "не помогает"
		case sc.Fails > sc.Wins:
			dot, verdict = LevelWarn, "спорно"
		}
		row := Component{Type: "row", Layout: "wide", Level: dot, Dot: levelDot(dot), ID: "score-" + sc.Name,
			Subtitle: k.Title,
			Caption: fmt.Sprintf("подмешивался %s · %d работ закончились удачно · %d сорвались%s",
				plural(sc.Used, "раз", "раза", "раз"), sc.Wins, sc.Fails, savedTail(saved[sc.Name])),
			Badges: []Badge{{Text: verdict, Color: dot}}}
		if verdict == "не помогает" && k.Enabled {
			row.Actions = []Action{{Action: "toggle_skill", Target: k.Name, Label: "Выключить"},
				{Action: "$goto", Target: engine.SkillScreen(k.Name), Label: "Открыть"}}
		} else {
			row.Actions = []Action{{Action: "$goto", Target: engine.SkillScreen(k.Name), Label: "Открыть"}}
		}
		useful = append(useful, row)
	}
	if len(useful) == 0 {
		useful = append(useful, Component{Type: "row", Layout: "wide", Subtitle: "Пока не по чему судить",
			Caption: "мера пользы считается по работам, в которые скилл подмешался"})
	}
	add(Component{Type: "section", Title: "Польза скиллов · неделя", Children: useful})

	// что решено за неделю
	var history []Component
	for _, state := range []string{store.DraftAccepted, store.DraftRejected} {
		list, _ := st.SkillDrafts(state, 10)
		for _, d := range list {
			if d.Decided < week.Format(time.RFC3339) {
				continue
			}
			dot, what := LevelOK, "принято"
			if state == store.DraftRejected {
				dot, what = "", "отклонено"
			}
			history = append(history, Component{Type: "row", Layout: "wide", Level: dot, Dot: levelDot(dot),
				Subtitle: skillDraftTitle(d),
				Caption:  what + " " + whenText(d.Decided) + " · " + d.Who,
				Detail:   d.Diff, Expandable: d.Diff != ""})
		}
	}
	if len(history) > 0 {
		add(Component{Type: "section", Title: "Решено за неделю", Children: history})
	}

	// пределы
	learn, judge := "off", "off"
	if eng.LearnOn() {
		learn = "on"
	}
	if eng.LearnJudgeOn() {
		judge = "on"
	}
	add(Component{Type: "section", Title: "Пределы", Children: []Component{
		{Type: "row", Layout: "wide", ID: "learn-on", Subtitle: "Учиться из законченных работ", Toggle: learn,
			Caption: "черновик рождается только после удачного и непростого дела",
			Actions: []Action{{Action: "toggle_learn", Label: "toggle"}}},
		{Type: "row", Layout: "wide", ID: "learn-judge", Subtitle: "Спрашивать судью на другой модели", Toggle: judge,
			Caption: "советует принять или нет; решение всё равно ваше",
			Actions: []Action{{Action: "toggle_learn_judge", Label: "toggle"}}},
	}})
	// пороги — «Тонкая настройка» экрана (settings_rare.go)
	add(Component{Type: "form", ID: "learn-limits", Title: "Пороги", Tier: tierRare, Style: formRows,
		Fields: []Field{
			s.settingField("learn.max_per_day", "max", "Не больше предложений в сутки", "", ""),
			s.settingField("learn.min_score", "score", "Порог «работа была непростой»",
				"Шаг модели — 1 очко, команда — 2, ошибка по дороге — 2", "очков"),
		},
		Submit: &Action{Action: "set_learn_limits", Label: "Сохранить"}})
	return nil
}

func skillDraftTitle(d *store.SkillDraft) string {
	what := "правка"
	if d.IsNew {
		what = "новый скилл"
	}
	return "Скилл «" + d.Title + "» · " + what
}

func draftRow(d *store.SkillDraft) Component {
	kind := map[string]string{
		store.DraftChat:       "из переписки",
		store.DraftExperience: "из опыта",
		store.DraftFix:        "из ошибки",
	}[d.Kind]
	badges := []Badge{{Text: map[bool]string{true: "новый", false: "правка"}[d.IsNew],
		Color: map[bool]string{true: "#1E7A32", false: "#B25000"}[d.IsNew]}}
	if kind != "" {
		badges = append(badges, Badge{Text: kind, Color: "#0058B0"})
	}
	caption := d.Who
	if d.Why != "" {
		caption += " · " + d.Why
	}
	if d.Judge != "" {
		caption += "\nсудья — " + d.Judge
	}
	actions := []Action{
		{Action: "decide_skill", Target: d.ID + "~accept", Label: "Принять", Style: "primary"},
		{Action: "decide_skill", Target: d.ID + "~reject", Label: "Отклонить"},
	}
	if d.Chat > 0 {
		actions = append(actions, Action{Action: "$goto", Target: fmt.Sprintf("chat:%d", d.Chat), Label: "Открыть чат"})
	}
	return Component{Type: "row", Layout: "wide", ID: "draft-" + d.ID, Level: LevelWarn, Dot: levelDot(LevelWarn),
		Subtitle: skillDraftTitle(d), Caption: caption, Detail: d.Diff, Expandable: true, Actions: actions}
}

func (s *Server) learningActions(req actionRequest) (string, error) {
	switch req.Action {
	case "toggle_learn":
		return learningScreen, s.eng.SetLearnOn(!s.eng.LearnOn())
	case "toggle_learn_judge":
		return learningScreen, s.eng.SetLearnJudge(!s.eng.LearnJudgeOn())
	case "set_learn_limits":
		// что пришло, то и проверяется: непришедшее поле — «не трогали»
		return learningScreen, s.saveSettings(req,
			settingParam{"max", "learn.max_per_day", "предложений в сутки"},
			settingParam{"score", "learn.min_score", "порог непростой работы"})
	}
	return "", errUnhandled
}

// savedTail — приписка про предотвращённое. Пусто, когда предотвращать было
// нечего: пустой счётчик у каждой строки только мешает читать.
func savedTail(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" · %s предотвращено", plural(n, "раз", "раза", "раз"))
}
