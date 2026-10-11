package api

// «Telegram → Собеседники»: права чатов одной матрицей (chat_rights.go).
//
// Шестнадцать собеседников были шестнадцатью раскрытиями по семь тумблеров,
// и ответить на вопрос «кому из них можно отвечать» значило открыть все.
// Теперь строка — чат, колонка — право, клетка переключает его тем же
// действием, что был тумблер (`tg_chat_right`, `tg_topic_right`,
// `tg_wait_toggle`, `tg_chat_class`). Группа с темами несёт темы вложенными
// строками: у них клетка показывает действующее право — своё или группы (пунктир
// у чужого) — и нажатие делает его своим; «Как у группы» в «···» снимает
// переопределения.
//
// Права, которых у такого чата не бывает, — клетка «na» с причиной, а не
// тумблер, который откажет после нажатия: админом — только личный чат, отвечать
// каналу некому, публиковать в канал можно только с правом постить.
//
// Поиск и отбор идут на сервер параметрами экрана (как у списка моделей): ищется
// по всем чатам, а не по показанным; показывается порция, остальное — по «Показать
// ещё». Само действие сервер не меняет: проверки прав остались в обработчиках.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// tgChatsPage — сколько чатов матрица показывает с первого раза.
const tgChatsPage = 30

// tgSearchHint — подсказка верхнего поля на «Собеседниках»: оно ищет чаты, а не
// настройки.
const tgSearchHint = "Найти чат: имя или ник"

// Отборы матрицы. «Все» не отбирает ничего.
const (
	tgFilterAll      = "all"
	tgFilterPeople   = "people"
	tgFilterGroups   = "groups"
	tgFilterChannels = "channels"
	tgFilterGranted  = "granted"
)

// tgGranted — у чата выданы права: хоть что-то из «читать», «отвечать», «писать»,
// «файлы», «админ». Блокировка — не право, а запрет.
func tgGranted(c *store.TGChat) bool {
	return c.Collect || c.Reply || c.Send || c.Files || c.Admin
}

// tgFilters — чипы отбора по порядку.
var tgFilters = []struct {
	ID, Title string
	Match     func(*store.TGChat) bool
}{
	{tgFilterAll, "Все", func(*store.TGChat) bool { return true }},
	{tgFilterPeople, "Личные", func(c *store.TGChat) bool { return c.Kind == "user" }},
	{tgFilterGroups, "Группы", func(c *store.TGChat) bool { return c.Kind == "group" }},
	{tgFilterChannels, "Каналы", func(c *store.TGChat) bool { return c.Kind == "channel" }},
	{tgFilterGranted, "С правами", tgGranted},
}

// tgKindWord — как вид чата называет человек и как по нему ищут.
var tgKindWord = map[string]string{"user": "личный", "group": "группа", "channel": "канал"}

// tgRightColumns — колонки матрицы по порядку. «Писать первым» у канала —
// «публиковать»: это одно право `send`, и подсказка клетки называет его по виду
// чата.
var tgRightColumns = []rightsColumn{
	{"collect", "Читать"},
	{"files", "Файлы"},
	{"reply", "Отвечать"},
	{"send", "Писать первым"},
	{"admin", "Админ"},
	{"blocked", "Не трогать"},
	{"wait", "Напоминать"},
}

// tgRightsLegend — что значит каждое право. Раньше это стояло подписью у каждого
// тумблера, и на шестнадцати чатах одни и те же фразы повторялись по сто раз; в
// матрице они стоят один раз, свёрнутой строкой под ней.
const tgRightsLegend = "Читать — сообщения идут в архив и в контекст; у канала это посты, свежие становятся разговорами с комментариями. " +
	"Файлы — вложения ложатся в «Файлы» Сёмы. " +
	"Отвечать — Сёма пишет вместо вас; по умолчанию выключено: молчание он показывает семафором в «Уведомлениях», а не заговаривает собеседника. " +
	"Писать первым — Сёма может начать разговор сам; у канала это публикация постом от вашего имени, и каждый пост Сёмы — черновик до вашей кнопки. " +
	"Админ — каждое сообщение из этого чата ваша команда Сёме, как в его собственном чате: письма, серверы, задачи, память; изменяющие команды всё равно ждут кнопки в «Уведомлениях». Включайте только чату со своим аккаунтом. " +
	"Не трогать — ни читать, ни отвечать, что бы ни просили; блокировка снимает остальные права. " +
	"Напоминать — Сёма не отвечает за вас, но показывает, что разговор ждёт ответа. " +
	"Класс чувствительности меняется в «···» строки: секретное облачной модели не уходит никогда, чувствительное — по настройке на «Что ушло наружу»; личные чаты и группы по умолчанию чувствительные, каналы обычные, и на строке класс виден капсулой только когда он другой. " +
	"У темы группы пунктир значит «как у группы»: нажатие делает право своим, а «Как у группы» в «···» возвращает наследование."

// tgChatsTab — вкладка «Собеседники»: отбор и поиск сверху, матрица прав, под
// ней — общее для каналов.
func (s *Server) tgChatsTab(doc *Document, add func(Component), acc *store.TGAccount, filter, search string, limit int) error {
	st := s.eng.Store()
	digits := engine.NumberDigits(acc.Phone)
	all, err := st.TGChats(acc.Phone, false)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		add(Component{Type: "section", Title: "Собеседники", Children: []Component{{Type: "row", Layout: "wide",
			Subtitle: "Список чатов пуст", Caption: "список читается из Telegram целиком, вместе с архивом",
			Actions: []Action{{Action: "tg_refresh_chats", Target: digits, Label: "Обновить список", Style: "primary"}}}}})
		return nil
	}

	// верхнее поле ищет чаты: «Найти настройку» здесь не нужен, настройки Telegram
	// видны на самом экране, а общий поиск по разделам остаётся над другими
	doc.Searchable, doc.SearchHint, doc.ownSearch = true, tgSearchHint, true
	counts := make([]int, len(tgFilters))
	for _, c := range all {
		for i, f := range tgFilters {
			if f.Match(c) {
				counts[i]++
			}
		}
	}
	active, match := tgFilterAll, tgFilters[0].Match
	for i, f := range tgFilters {
		doc.Filters = append(doc.Filters, Filter{ID: f.ID, Title: fmt.Sprintf("%s · %d", f.Title, counts[i])})
		if f.ID == filter {
			active, match = f.ID, f.Match
		}
	}
	doc.ActiveFilter = active
	query := active != tgFilterAll || strings.TrimSpace(search) != ""

	var found []*store.TGChat
	for _, c := range all {
		if match(c) && (strings.TrimSpace(search) == "" || looksLike(search, c.Title, c.Name, tgKindWord[c.Kind])) {
			found = append(found, c)
		}
	}
	granted := counts[len(counts)-1]
	title := fmt.Sprintf("Собеседники · %d, права выданы %d", len(all), granted)
	if query {
		title = fmt.Sprintf("Собеседники · нашлось %d из %d", len(found), len(all))
	}

	if len(found) == 0 {
		add(Component{Type: "section", ID: "tg-chats", Title: title, Children: []Component{emptyState("Под отбор ничего не подошло",
			"Ищется по имени, нику и виду («личный», «группа», «канал») среди "+plural(len(all), "чата", "чатов", "чатов")+" на сервере, не по показанным", "magnifyingglass")}})
	} else {
		shown := found
		if page := matrixPage(limit, tgChatsPage); len(shown) > page {
			shown = shown[:page]
		}
		var rows []rightsRow
		for _, c := range shown {
			rows = append(rows, s.tgChatRow(c))
			if c.Forum {
				rows = append(rows, s.tgTopicRows(c)...)
			}
		}
		add(rightsMatrix("tg-chats", title,
			"Нажатие переключает право; прочерк — права, которого у такого чата не бывает. Ничего не включается само: как и у серверов, разрешает человек.",
			tgRightColumns, rows))
		if len(shown) < len(found) {
			add(moreRows(len(shown), len(found)))
		}
		add(Component{Type: "section", ID: "tg-rights-how", Title: "Что значат права", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary", Text: tgRightsLegend}}})
	}
	s.tgChannelsCommon(add, all, acc)
	return nil
}

// tgChannelsCommon — то, что относится к каналам разом: «читать все» и общий срок
// «пост — разговор». Каналы в самой матрице строками; здесь только массовые кнопки,
// у обеих подтверждение, а блокированные каналы они не трогают.
func (s *Server) tgChannelsCommon(add func(Component), all []*store.TGChat, acc *store.TGAccount) {
	digits := engine.NumberDigits(acc.Phone)
	var channels, reading int
	for _, c := range all {
		if c.Kind == "channel" {
			channels++
			if c.Collect {
				reading++
			}
		}
	}
	if channels == 0 {
		add(Component{Type: "section", Title: "Каналы", Children: []Component{{Type: "row", Layout: "wide",
			Subtitle: "Каналов в списке нет",
			Caption:  "список читается из Telegram целиком, вместе с архивом; канал, на который вы подписаны, приходит вместе с чатами",
			Actions:  []Action{{Action: "tg_refresh_chats", Target: digits, Label: "Обновить список", Style: "primary"}}}}})
		return
	}
	postDays := s.eng.Store().SettingInt(settingDef("tg.post_days"))
	add(Component{Type: "section", ID: "tg-channels", Title: fmt.Sprintf("Каналы · %d, читает %d", channels, reading), Children: []Component{
		{Type: "row", Layout: "wide", ID: "tg-channels-all",
			Subtitle: "Все каналы разом",
			Caption:  "читать — посты каждого канала идут в архив, свежие становятся разговорами; «не трогать вовсе» массовая кнопка не снимает",
			Actions: []Action{
				{Action: "tg_channels_all", Target: digits + ".on", Label: "Читать все каналы", Style: "primary",
					Confirm:   fmt.Sprintf("Включить чтение у всех каналов (%d)? Сёма заберёт последние посты каждого — это заходы в Telegram и место в архиве.", channels),
					ConfirmOK: "Читать все"},
				{Action: "tg_channels_all", Target: digits + ".off", Label: "Не читать ни один",
					Confirm:   "Выключить чтение у всех каналов? Собранные посты останутся в архиве, разговоры уйдут в память сводкой.",
					ConfirmOK: "Не читать"},
			}},
		{Type: "row", Layout: "wide", ID: "tg-lim-tg.post_days",
			Subtitle: "Пост канала — разговор",
			Caption:  "общее для всех каналов: пост свежее этого срока становится разговором с комментариями, старше — остаётся историей канала; правится на «Ответы и пределы»",
			Facts:    []Prop{{Label: "Сейчас", Value: strconv.Itoa(postDays) + " дней"}, {Label: "Действует", Value: "на все каналы"}},
			Actions:  []Action{{Action: "$goto", Target: tgBase(acc) + "~" + tgTabAnswer, Label: "Изменить"}}},
	}})
}

// tgChatRow — строка чата: капсулы вида, подпись из ника и счёта собранного,
// клетки прав по виду чата.
func (s *Server) tgChatRow(c *store.TGChat) rightsRow {
	target := tgTarget(c.Account, c.ID)
	flag := func(right, what string, on bool) Cell {
		return rightCell(c.Title, what, on, Action{Action: "tg_chat_right", Target: target + "~" + right})
	}
	// подсказки клеток короткие: чат, право и состояние. Что право значит, сказано
	// один раз под матрицей (tgRightsLegend), а не в каждой из ста с лишним клеток
	cells := map[string]Cell{}
	cells["blocked"] = flag("blocked", "не трогать вовсе", c.Blocked)
	switch c.Kind {
	case "channel":
		cells["collect"] = flag("collect", "читать посты", c.Collect)
		cells["files"] = flag("files", "забирать файлы", c.Files)
		cells["reply"] = rightNA("В канале отвечать некому: комментарии живут в группе обсуждения")
		cells["admin"] = rightNA("Админом может быть только личный чат, а это канал")
		cells["wait"] = rightNA("Канал не ждёт ответа: посты идут потоком")
		switch {
		case c.CanPost || c.Send:
			// публиковать можно только там, где у аккаунта есть право постить: иначе
			// клетка обещала бы то, чего Telegram не даст. Выданное и потерянное
			// право снимается нажатием
			cells["send"] = flag("send", "публиковать в канал", c.Send)
		default:
			cells["send"] = rightNA("Публиковать нельзя: у вашего аккаунта нет права постить в этом канале — вы не владелец и не админ с постами. Выдали право — нажмите «Обновить список»")
		}
	default:
		cells["collect"] = flag("collect", "читать переписку", c.Collect)
		cells["files"] = flag("files", "забирать файлы", c.Files)
		cells["reply"] = flag("reply", "отвечать вместо вас", c.Reply)
		cells["send"] = flag("send", "писать первым", c.Send)
		if c.Kind == "user" {
			cells["admin"] = flag("admin", "админ: управлять Сёмой из этого чата", c.Admin)
		} else {
			cells["admin"] = rightNA("Админом может быть только личный чат, а это группа")
		}
		// семафор молчания: выключается и из самого уведомления кнопкой «Не
		// напоминать в этом чате», включается здесь
		cells["wait"] = rightCell(c.Title, "напоминать, если не ответил",
			!s.eng.TGWaitIsOff(c.Account, c.ID), Action{Action: "tg_wait_toggle", Target: target})
	}

	// класс чувствительности правят редко: он в «···» строки тремя пунктами, как
	// прежние три кнопки, а на самой строке стоит капсулой только когда отличается
	// от обычного для вида чата (у личного обычно «чувствительное», у канала —
	// «обычное»): капсула, одинаковая у всех строк, ничего не говорит
	class, usual := c.Class, store.ClassSensitive
	if c.Kind == "channel" {
		usual = store.ClassPlain
	}
	if class == "" {
		class = usual
	}
	var menu []Action
	for _, name := range []string{store.ClassPlain, store.ClassSensitive, store.ClassSecret} {
		label := "Класс «" + name + "»"
		if name == class {
			label += " · сейчас"
		}
		menu = append(menu, Action{Action: "tg_chat_class", Target: target + "~" + name, Label: label})
	}

	badges := []Badge{{Text: tgKindWord[c.Kind], Color: "#8E8E93"}}
	if c.Forum {
		badges = append(badges, Badge{Text: "топики", Color: "#8E8E93"})
	}
	if c.Kind == "channel" && c.CanPost {
		badges = append(badges, Badge{Text: "вы админ", Color: "#1E7A32"})
	}
	if class != usual {
		color := "#B25000"
		if class == store.ClassPlain {
			color = "#1E7A32"
		}
		badges = append(badges, Badge{Text: class, Color: color})
	}
	var facts []string
	if c.Name != "" {
		facts = append(facts, "@"+c.Name)
	}
	if c.Count > 0 {
		facts = append(facts, "собрано "+strconv.Itoa(c.Count))
	}
	level := ""
	switch {
	case c.Blocked:
		level = LevelAlert
	case tgGranted(c):
		level = LevelOK
	}
	return rightsRow{ID: "tg-" + strconv.FormatInt(c.ID, 10), Title: c.Title, Caption: strings.Join(facts, " · "),
		Badges: badges, Level: level, Cells: cells, Menu: menu}
}

// tgTopicRows — темы группы вложенными строками. Клетка показывает действующее
// право: своё у темы или, пока тема его не переопределила, право группы (пунктир).
// Нажатие делает право своим; «Как у группы» в «···» возвращает наследование.
// Админ, напоминание и класс — у группы целиком: у темы их клеток нет.
func (s *Server) tgTopicRows(c *store.TGChat) []rightsRow {
	var rows []rightsRow
	target := tgTarget(c.Account, c.ID)
	for _, t := range s.eng.TGTopicsOf(c.Account, c.ID) {
		eff := s.eng.TGEffective(c, t.ID)
		tid := target + "/" + strconv.Itoa(t.ID)
		own := t.Collect != store.TopicInherit || t.Reply != store.TopicInherit || t.Send != store.TopicInherit ||
			t.Files != store.TopicInherit || t.Blocked != store.TopicInherit
		name := c.Title + " · " + t.Title
		topic := func(right, what string, on bool, mine int) Cell {
			cell := rightCell(name, what, on, Action{Action: "tg_topic_right", Target: tid + "~" + right})
			if mine == store.TopicInherit {
				cell.Inherited = true
				cell.Title = name + " · " + what + ": как у группы (" + map[bool]string{true: "включено", false: "выключено"}[on] + ")"
			}
			return cell
		}
		cells := map[string]Cell{
			"collect": topic("collect", "читать переписку", eff.Collect, t.Collect),
			"files":   topic("files", "забирать файлы", eff.Files, t.Files),
			"reply":   topic("reply", "отвечать вместо вас", eff.Reply, t.Reply),
			"send":    topic("send", "писать первым", eff.Send, t.Send),
			"blocked": topic("blocked", "не трогать вовсе", eff.Blocked, t.Blocked),
			"admin":   rightNA("Админом может быть только личный чат, а тема — часть группы"),
			"wait":    rightNA("Напоминание о молчании — у группы целиком: его выключают у самой группы"),
		}
		caption := "как у группы"
		var menu []Action
		if own {
			caption = "свои права"
			menu = []Action{{Action: "tg_topic_inherit", Target: tid, Label: "Как у группы: снять свои права"}}
		}
		level := ""
		switch {
		case eff.Blocked:
			level = LevelAlert
		case eff.Collect || eff.Reply || eff.Send || eff.Files:
			level = LevelOK
		}
		rows = append(rows, rightsRow{ID: "tg-" + strconv.FormatInt(c.ID, 10) + "-" + strconv.Itoa(t.ID), Title: t.Title,
			Caption: caption, Level: level, Nested: true, Cells: cells, Menu: menu})
	}
	return rows
}
