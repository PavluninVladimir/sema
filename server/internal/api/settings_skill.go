package api

// Экран одного скилла: `settings:skills@<имя>`. Шапку (путь назад, закреплённая
// строка) рисует общий entityFrame, содержимое — здесь:
//
//   - предложение правки, ждущее решения, — вверху;
//   - форма строками настройки: название, блок, слова-приметы, «когда применять» и
//     текст; сохраняет нижняя полоса действием `save_skill` (прежним);
//   - откуда скилл и правил ли его человек: встроенный, пока его не правили,
//     обновляется вместе со сборкой, а после правки — нет;
//   - польза: сколько раз подмешивался, чем кончились работы за неделю, какие
//     модули его берут;
//   - «Опасное» внизу своей карточкой: удалить свой скилл или вернуть встроенный
//     текст, с вопросом.
//
// **Полный текст скилла едет только сюда**: в списке его нет вовсе (разбор 1
// октября 2026: 51 КБ текста в кнопках «Скопировать» каждой строки). «Скопировать
// текст» — в «···» шапки этого экрана.

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// skillCaption — одна строка фактов закреплённой шапки: блок, откуда, размер.
func skillCaption(k *store.Skill) string {
	parts := []string{}
	if k.Group != "" {
		parts = append(parts, k.Group)
	}
	parts = append(parts, skillSourceWord(k))
	parts = append(parts, "≈"+strconv.Itoa(tokensRough(k.Text))+" токенов")
	return strings.Join(parts, " · ")
}

func (s *Server) buildSkill(doc *Document, name string) error {
	doc.Layout = "read"
	st := s.eng.Store()
	k, ok := st.Skill(name)
	if !ok {
		// скилл удалили, пока экран был открыт: не ошибка, а объяснение
		doc.Heading = "Скиллы"
		doc.Components = append(doc.Components,
			Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: skillsScreen, Label: "‹ Скиллы"}}},
			emptyState("Скилла «"+name+"» нет", "Его удалили или не заводили. Список — по кнопке «‹ Скиллы» выше", "doc.text"))
		return nil
	}
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	// ---- предложение правки: ждёт решения, стоит вверху ----
	if drafts, err := st.SkillDrafts(store.DraftOpen, 200); err == nil {
		var waiting []Component
		for _, d := range drafts {
			if d.Name != k.Name {
				continue
			}
			what := "правка"
			if d.IsNew {
				what = "новый текст"
			}
			waiting = append(waiting, Component{Type: "row", Layout: "wide", ID: "skill-draft-" + d.ID,
				Level: LevelWarn, Dot: levelDot(LevelWarn),
				Subtitle: "Предложена " + what + " — ждёт вашего слова",
				Caption:  d.Who + " · " + whenText(d.Created) + " · до согласия в запрос не попадает ничего",
				Actions:  []Action{{Action: "$goto", Target: learningScreen, Label: "Посмотреть", Style: "primary"}}})
		}
		if len(waiting) > 0 {
			add(Component{Type: "section", ID: "skill-alerts", Children: waiting})
		}
	}

	// ---- форма: название, блок, приметы, «когда», текст ----
	hint := "markdown, по-русски; сохранение применяется сразу"
	if k.Source == store.SkillBuiltin && k.EditedBy == "" {
		hint += " · после правки сборка этот текст обновлять не будет"
	}
	add(Component{Type: "form", ID: "skill-form", Style: formRows,
		Fields: []Field{
			{Type: "text", Name: "title", Label: "Название", Value: k.Title},
			{Type: "text", Name: "group", Label: "Блок", Value: k.Group},
			{Type: "text", Name: "keywords", Label: "Слова-приметы", Hint: "через запятую; для «Администрирование» ещё и имена серверов", Value: k.Keywords},
			{Type: "text", Name: "when", Label: "Когда применять", Hint: "одна строка для описи в промпте", Value: k.When},
			{Type: "code", Name: "text", Label: "Текст", Hint: hint, Value: k.Text},
		},
		Submit: &Action{Action: "save_skill", Target: k.Name, Label: "Сохранить"}})

	// ---- откуда ----
	var origin []Component
	source := skillSourceWord(k)
	switch {
	case k.Source == store.SkillBuiltin && k.EditedBy == "":
		source += " · приезжает со сборкой сервера и обновляется вместе с ней, пока его не правили"
	case k.Source == store.SkillBuiltin:
		source += " · правили: сборка этот текст больше не обновляет"
	case k.Source == store.SkillChat:
		source += " · предложен в чате и принят вами"
	default:
		source += " · ваш"
	}
	origin = append(origin, Component{Type: "row", Layout: "wide", ID: "skill-source", Subtitle: "Источник", Caption: source})
	switch {
	case k.EditedBy != "":
		origin = append(origin, Component{Type: "row", Layout: "wide", ID: "skill-edited", Subtitle: "Правил",
			Caption: k.EditedBy + " · " + whenText(k.EditedAt)})
	case k.Source == store.SkillBuiltin && k.EditedAt != "":
		origin = append(origin, Component{Type: "row", Layout: "wide", ID: "skill-edited", Subtitle: "Обновлён из сборки",
			Caption: whenText(k.EditedAt)})
	}
	origin = append(origin, Component{Type: "row", Layout: "wide", ID: "skill-size", Subtitle: "Размер",
		Caption: "≈" + plural(tokensRough(k.Text), "токен", "токена", "токенов") + " · в запрос полным текстом попадает по приметам и в пределе " +
			plural(s.eng.SkillsMaxTokens(), "токен", "токена", "токенов") + " на все скиллы разом"})
	add(Component{Type: "section", ID: "skill-origin", Title: "Откуда", Children: origin})

	// ---- польза ----
	var useful []Component
	line := "ни разу не подмешивался"
	if k.Uses > 0 {
		line = "подмешивался " + plural(k.Uses, "раз", "раза", "раз") + " · последний раз " + whenText(k.LastUsed)
	}
	useful = append(useful, Component{Type: "row", Layout: "wide", ID: "skill-uses", Subtitle: "Подмешивался", Caption: line})
	week := time.Now().AddDate(0, 0, -7).Format(time.RFC3339)
	if scores, err := st.SkillScores(week); err == nil {
		saved := st.SkillSaved(week)
		for _, sc := range scores {
			if sc.Name != k.Name {
				continue
			}
			caption := plural(sc.Used, "работа", "работы", "работ") + ": " + strconv.Itoa(sc.Wins) + " закончились удачно, " +
				strconv.Itoa(sc.Fails) + " сорвались" + savedTail(saved[sc.Name])
			row := Component{Type: "row", Layout: "wide", ID: "skill-score", Subtitle: "За неделю", Caption: caption}
			switch {
			case sc.Wins == 0 && sc.Used >= 5:
				withLevel(&row, LevelAlert)
				row.Badges = []Badge{{Text: "не помогает", Level: LevelAlert}}
			case sc.Fails > sc.Wins:
				withLevel(&row, LevelWarn)
				row.Badges = []Badge{{Text: "спорно", Level: LevelWarn}}
			default:
				withLevel(&row, LevelOK)
				row.Badges = []Badge{{Text: "помогает", Level: LevelOK}}
			}
			useful = append(useful, row)
		}
	}
	if users := s.skillUsers()[k.Name]; len(users) > 0 {
		useful = append(useful, Component{Type: "row", Layout: "wide", ID: "skill-modules", Subtitle: "Берут модули", Caption: strings.Join(users, ", "),
			Actions: []Action{{Action: "$goto", Target: "modules", Label: "Модули"}}})
	}
	add(Component{Type: "section", ID: "skill-useful", Title: "Польза", Children: useful})

	// ---- опасное ----
	var danger []Component
	if _, builtin := engine.BuiltinSkillText(k.Name); builtin {
		if k.EditedBy != "" {
			danger = append(danger, Component{Type: "row", Layout: "wide", ID: "danger-reset",
				Subtitle: "Вернуть встроенный текст",
				Caption:  "Ваши правки пропадут, а скилл снова будет обновляться вместе со сборкой",
				Actions: []Action{{Action: "reset_skill", Target: k.Name, Label: "Вернуть…", Style: "destructive",
					Confirm: "Вернуть встроенный текст скилла «" + k.Title + "»? Ваши правки пропадут.", ConfirmOK: "Вернуть"}}})
		}
	}
	if k.Source != store.SkillBuiltin {
		danger = append(danger, Component{Type: "row", Layout: "wide", ID: "danger-delete",
			Subtitle: "Удалить скилл",
			Caption:  "Он перестанет попадать в чат, а модули, которые его просят, получат ошибку",
			Actions: []Action{{Action: "delete_skill", Target: k.Name, Label: "Удалить…", Style: "destructive",
				Confirm: "Удалить скилл «" + k.Title + "»?", ConfirmOK: "Удалить"}}})
	}
	if len(danger) > 0 {
		add(Component{Type: "section", ID: "skill-danger", Title: "Опасное", Style: "danger", Children: danger})
	}

	// ---- шапка ----
	e := entity{Section: skillsScreen, Back: "Скиллы", ID: k.Name, Name: k.Title,
		Avatar: firstRuneOfName(k.Title), AvatarColor: "#7952B3", Caption: skillCaption(k), Switch: "Скилл"}
	if !k.Enabled {
		e.Badges = []Badge{{Text: "выключен", Level: LevelWarn}}
	}
	e.Badges = append(e.Badges, Badge{Text: skillSourceWord(k)})
	if k.Enabled {
		e.Primary = &Action{Action: "toggle_skill", Target: k.Name, Label: "Выключить"}
	} else {
		e.Primary = &Action{Action: "toggle_skill", Target: k.Name, Label: "Включить", Style: "primary"}
	}
	e.Menu = []Action{
		// копирование идёт по имени поля, а не носит второй раз текст в самой кнопке
		{Action: "$copy_field", Target: "text", Label: "Скопировать текст"},
		{Action: "$goto", Target: learningScreen, Label: "Развитие"},
	}
	for _, other := range s.eng.Skills() {
		e.Others = append(e.Others, other.Name)
	}
	sort.Strings(e.Others)
	entityFrame(doc, e)
	return nil
}

// buildSkillNew — «Добавить скилл»: свой экран, а не форма под списком из тридцати
// строк. Имя, название, блок, приметы, «когда» и текст; что вписано — то и уйдёт в
// `add_skill` (имена полей прежние).
func (s *Server) buildSkillNew(doc *Document) error {
	doc.Heading = "Добавить скилл"
	doc.Subheading = "Инструкция Сёме на русском: когда её применять и что делать. Ключи и пароли в текст не сохраняются"
	doc.Layout = "read"
	doc.Components = append(doc.Components,
		Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: skillsScreen, Label: "‹ Скиллы"}}},
		Component{Type: "form", ID: "skill-new", Style: formRows,
			Fields: []Field{
				{Type: "text", Name: "name", Label: "Имя", Hint: "латиница и дефис: backups; «new» занято"},
				{Type: "text", Name: "title", Label: "Название", Hint: "например Резервные копии"},
				{Type: "text", Name: "group", Label: "Блок", Value: "Администрирование"},
				{Type: "text", Name: "keywords", Label: "Слова-приметы", Hint: "через запятую; для «Администрирование» ещё и имена серверов"},
				{Type: "text", Name: "when", Label: "Когда применять", Hint: "одна строка для описи в промпте"},
				{Type: "code", Name: "text", Label: "Текст", Hint: "инструкция на русском, markdown"},
			},
			Submit: &Action{Action: "add_skill", Label: "Добавить", Style: "primary"}})
	return nil
}
