package api

// «Вы меняли» и «Под рукой» на Обзоре настроек.
//
// Обзор отвечает на «что ждёт меня, живо ли подключённое» — и на два вопроса про
// самого человека: что он менял и что он меняет постоянно. До 2 октября 2026 тут
// стоял блок «Изменения настроек» с пятью последними записями журнала, и в нём
// были `chat.read.71` и `folders.cursor.vp.INBOX`: курсоры Сёмы, а не правки
// человека. Теперь:
//
//   - **«Вы меняли»** — последние правки, подписанные человеком, служебные отметки
//     не в счёт (`settings_log.marks`), с «было → стало», человеческим именем из
//     индекса и переходом к строке настройки;
//   - **«Под рукой»** — пять настроек, которые человек менял чаще всех за месяц
//     (журнал считает), живыми контролами прямо на Обзоре: тот же контрол, что на
//     экране настройки, то же действие сохранения, те же проверки. Что опасно
//     менять «с ходу» — вход и доступ, доверие, самостоятельность серверов,
//     «Опасное», секреты — сюда не попадает никогда, даже если эти настройки
//     менялись чаще всего: признак `Careful` стоит у настройки в реестре и у поля в
//     индексе, а не списком имён в этом файле.

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	// recentEditsN — сколько последних правок на Обзоре
	recentEditsN = 5
	// handyN — сколько настроек «Под рукой»
	handyN = 5
	// handyDays — за какой срок считаются правки: «за последний месяц»
	handyDays = 30
	// handyScan — сколько самых частых параметров просматривается: часть из них
	// на Обзор не попадёт (осторожные, без экрана), и пять первых подряд бывает мало
	handyScan = 60
)

// recentEdits — блок «Вы меняли».
func (s *Server) recentEdits(add func(Component), guest bool) {
	st := s.eng.Store()
	list, err := st.SettingsLogView(store.SettingsLogMine, recentEditsN)
	// заведённые ключи и токены — не правка настройки, но человеческое действие
	// того же порядка, и блок «Действия с доступом» с Обзора ушёл вместе с ними
	// (accessEvent): гостю доступ не показывается
	var events []accessEvent
	if !guest {
		events = s.accessEvents(recentEditsN)
	}
	var rows []Component
	if (err == nil && len(list) > 0) || len(events) > 0 {
		type item struct {
			when string
			row  func() Component
		}
		var merged []item
		idx := s.settingsIndex()
		// события доступа — первыми: при равном времени (секунда) новый ключ или токен
		// не должен уступить правке настройки, их считают единицами за месяц
		for i, ev := range events {
			ev, i := ev, i
			merged = append(merged, item{ev.When, func() Component {
				return Component{Type: "row", Layout: "wide", ID: "доступ-" + strconv.Itoa(i), Subtitle: ev.What,
					Caption: whenText(ev.When) + " · Вход и доступ",
					Actions: []Action{{Action: "$goto", Target: ev.Addr, Label: "К ключам"}}}
			}})
		}
		for _, chg := range list {
			chg := chg
			merged = append(merged, item{chg.At, func() Component { return s.namedEditRow(idx, chg, true) }})
		}
		sort.SliceStable(merged, func(i, j int) bool { return merged[i].when > merged[j].when })
		if len(merged) > recentEditsN {
			merged = merged[:recentEditsN]
		}
		for _, m := range merged {
			rows = append(rows, m.row())
		}
	} else {
		rows = append(rows, emptyState("Вы пока ничего не меняли",
			"Сюда встанут последние пять ваших правок: что поменяли, было и стало, и переход к самой настройке. "+
				"Служебные отметки Сёмы — курсоры папок, «прочитано до», последние ошибки — сюда не попадают", "slider.horizontal.3"))
	}
	mine, service, _ := st.CountSettingsLogView()
	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "changes-all",
		Subtitle: "Весь журнал настроек",
		Caption: "ваших правок " + strconv.Itoa(mine) + ", служебных отметок Сёмы " + strconv.Itoa(service) +
			" — отдельным отбором; значения секретов не пишутся",
		Actions: []Action{{Action: "$goto", Target: changesScreen, Label: "Открыть журнал →"}}})
	add(Component{Type: "section", ID: "recent-edits", Title: "Вы меняли", Children: rows})
}

// handySettings — блок «Под рукой».
func (s *Server) handySettings(add func(Component)) {
	st := s.eng.Store()
	since := time.Now().AddDate(0, 0, -handyDays).Format(time.RFC3339)
	top, err := st.TopEditedKeys(since, handyScan)
	if err != nil {
		top = nil
	}
	var rows []Component
	building := false
	if len(top) > 0 {
		idx := s.settingsIndex()
		building = idx == nil
		if building {
			// индекс собирается в фоне (perf-pass 4.3): Обзор его не ждёт
			top = nil
		}
		seen := map[string]bool{}
		for _, k := range top {
			e := idx.pick(pinnables(idx.byKey[k.Key]))
			if e == nil || seen[e.Key] {
				continue
			}
			seen[e.Key] = true
			rows = append(rows, s.handyRow(e, k.Count))
			if len(rows) == handyN {
				break
			}
		}
	}
	if building {
		rows = append(rows, Component{Type: "text", Style: "secondary", ID: indexBuildingID,
			Text: "Индекс настроек собирается — частые настройки встанут сюда через несколько секунд, экран перечитается сам"})
	}
	if len(rows) == 0 {
		rows = append(rows, emptyState("Здесь появятся настройки, которые вы меняете чаще всего",
			"Пять настроек, которые вы правили чаще других за месяц, встанут сюда живыми контролами: тумблер, число, список — "+
				"тем же, что на их экране. Вход и доступ, доверие, самостоятельность серверов и секреты сюда не попадают никогда: "+
				"их правят там, где рядом стоит объяснение", "slider.horizontal.3"))
	}
	// Style «stack»: формы лежат одной карточкой, а не пятью рамками (словарь 31)
	add(Component{Type: "section", ID: "handy", Style: "stack", Title: "Под рукой", Children: rows})
}

// pinnables — из записей одного ключа те, что можно вынести на Обзор.
func pinnables(list []*indexEntry) []*indexEntry {
	var out []*indexEntry
	for _, e := range list {
		if e.pinnable() {
			out = append(out, e)
		}
	}
	return out
}

// handyRow — настройка на Обзоре: то же поле, что на её экране (строкой
// настройки, живое значение из реестра), форма с тем же действием сохранения. Её
// сохраняет нижняя полоса Обзора, как любую другую форму настроек.
func (s *Server) handyRow(e *indexEntry, count int) Component {
	hint := pathWords(e.Path)
	if e.Hint != "" {
		hint += " · " + e.Hint
	}
	field := s.settingField(e.Key, e.Param, e.Title, hint, e.Unit)
	return Component{Type: "form", ID: "pin-" + e.Key, Style: formRows,
		Fields: []Field{field},
		Submit: &Action{Action: e.Action, Target: e.Target, Label: "Сохранить"}}
}

// namedEditRow — одна правка журнала с человеческим именем: путь над названием,
// когда и кто под ним, «было → стало»; ключ мелко в самом низу. Строка ведёт к
// настройке: с подсветкой, если индекс знает поле, на экран — если знает только его.
//
// compact — строка для Обзора: три строки вместо пяти. Правки там все ваши, поэтому
// «кто» не пишется, путь стоит рядом со временем («вчера 23:10 · Серверы → srv-vpn»), а
// ключ остаётся для журнала.
func (s *Server) namedEditRow(idx *settingsIndex, chg store.SettingChange, compact bool) Component {
	name := idx.nameOf(chg)
	who := chg.Who
	if who == "" {
		who = "Сёма сам"
	}
	c := Component{Type: "row", Layout: "wide", ID: "правка-" + strconv.FormatInt(chg.ID, 10),
		Title: name.Path, Subtitle: name.Title, Caption: whenText(chg.At) + " · " + who}
	if compact {
		c.Title, c.Caption = "", whenText(chg.At)
		if name.Path != "" {
			c.Caption += " · " + name.Path
		}
	} else if name.Title != chg.Key {
		c.Meta = chg.Key
	}
	before, after := editShown(name, chg)
	switch {
	case chg.Note != nil:
		// составная настройка: «Было → стало» только у того, что показывать можно
		// (цена, модель роли, срок); у ключа, кода и списков значений нет, и пустые
		// «Было / Стало» читались бы как «пусто»
		if chg.Note.Was != "" || chg.Note.Now != "" {
			c.Facts = []Prop{{Label: "Было", Value: chg.Note.Was}, {Label: "Стало", Value: chg.Note.Now}}
		}
		if chg.Removed {
			c.Badges = []Badge{{Text: "параметр убран", Level: LevelInfo}}
		}
	case chg.Removed:
		c.Facts = []Prop{{Label: "Было", Value: before}}
		c.Badges = []Badge{{Text: "параметр убран", Level: LevelInfo}}
	default:
		c.Facts = []Prop{{Label: "Было", Value: before}, {Label: "Стало", Value: after}}
	}
	if chg.Hidden {
		c.Badges = append(c.Badges, Badge{Text: "секрет", Level: LevelInfo})
	}
	if name.Addr != "" {
		c.Actions = []Action{{Action: "$goto", Target: name.Addr, Label: "К настройке"}}
	}
	return c
}

// editShown — «было» и «стало» словами: флаг — «включено», число — с единицей, а
// пустое у настройки с умолчанием — «по умолчанию»: записи не было, и это не то
// же самое, что «пусто».
func editShown(name settingName, chg store.SettingChange) (before, after string) {
	if chg.Hidden {
		return editValue(chg.Before, true), editValue(chg.After, true)
	}
	e := name.Entry
	if e == nil || e.Key == "" {
		return editValue(chg.Before, false), editValue(chg.After, false)
	}
	_, registered := store.LookupSetting(e.Key)
	show := func(raw string) string {
		if strings.TrimSpace(raw) == "" {
			if registered {
				return "по умолчанию"
			}
			return "не задано" // умолчание у такой настройки живёт в коде, а не в реестре
		}
		if e.Invert {
			// тумблер читается наоборот настройке («выполняет» ↔ «вхолостую»)
			raw = boolValue(!truthy(raw))
		}
		return formatValue(e.Control, e.Unit, raw)
	}
	return show(chg.Before), show(chg.After)
}
