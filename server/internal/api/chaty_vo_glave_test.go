package api

import (
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Чаты во главе угла: Сёма открывается разговором, а не разбором почты,
// пункт «Чаты» стоит первым и несёт счётчик ждущих, а отдельного пункта
// «Ждут вас» в меню нет — всё, что ждёт решения, приходит карточкой в
// разговор с Сёмой, туда же, где человек отвечает.
func TestChatsComeFirst(t *testing.T) {
	s, st := testServer(t)
	conversation, _ := st.CreateChat("Telegram · Вадим")
	сёма, _ := st.CreateThread(conversation, store.ChatKindSema, 0, "Сёма · Вадим")
	_ = st.AddChatMsg(сёма, "assistant", "утренняя сводка готова")

	doc, err := s.buildScreen("", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}

	// пустой адрес открывает разговор с Сёмой — и в меню видно, какой
	// именно: адрес «ask» сам по себе не говорит, где человек стоит
	if doc.Active != "chat:"+strconv.FormatInt(сёма, 10) {
		t.Errorf("открыт не разговор с Сёмой, а %q", doc.Active)
	}

	// «Чаты» — первый пункт меню, а «Уведомления» — сразу за ними: владелец
	// вернул пункт в меню 17 сентября 2026
	if len(doc.Screens) < 2 || doc.Screens[0].ID != "ask" {
		t.Fatalf("первый пункт меню не «Чаты»: %+v", doc.Screens)
	}
	if doc.Screens[1].ID != notifyScreen {
		t.Errorf("сразу за «Чатами» не «Уведомления», а %q", doc.Screens[1].Title)
	}

	// разговоры видны под пунктом «Чаты», и первым — тот, что открыт:
	// иначе человек не видит, где стоит
	children := doc.Screens[0].Children
	if len(children) == 0 {
		t.Fatal("под «Чатами» не видно разговоров")
	}
	// первым — разговор с Сёмой. Строки «Все разговоры» нет: адрес узла
	// открывал тот же разговор, что и первый ребёнок, — двойник
	if children[0].Title != "Сёма · Вадим" {
		t.Errorf("первым под «Чатами» стоит %q, а не разговор с Сёмой", children[0].Title)
	}
	for _, д := range children {
		if д.ID == "ask" {
			t.Errorf("под «Чатами» остался пункт-двойник %q", д.Title)
		}
	}
	// и безымянной строки «Обзор» у узла нет
	if !doc.Screens[0].NoSelf {
		t.Error("у «Чатов» рисуется строка «Обзор» — она двойник разговора с Сёмой")
	}
	п := toMenuItem(doc.Screens[0], "ask", "")
	if п.Addr != "" {
		t.Errorf("у узла «Чаты» есть свой адрес %q — клиент нарисует «Обзор»", п.Addr)
	}
	// открыт разговор с Сёмой — подсвечен он
	for _, ребёнок := range п.Children {
		if ребёнок.Selected && ребёнок.Name != "Сёма · Вадим" {
			t.Errorf("подсвечен %q, а открыт разговор с Сёмой", ребёнок.Name)
		}
	}
}

// Где ждёт запись — видно точкой уровня на самом разговоре, а сколько
// их — сказано словами в подписи разговора. Числа в меню быть не может:
// цифра рядом с названием чата читается как непрочитанные сообщения.
func TestWhereRecordWaitsShownByDot(t *testing.T) {
	s, st := testServer(t)
	conversation, _ := st.CreateChat("Telegram · Вадим")
	сёма, _ := st.CreateThread(conversation, store.ChatKindSema, 0, "Сёма · Вадим")
	_ = st.AddChatMsg(сёма, "assistant", "смотрю")
	if _, err := st.PutNotification(&store.Notification{
		Key: "servers:daily", Source: "module:watch", Level: "warn",
		Title: "Требует внимания", State: store.NotifyOpen,
	}); err != nil {
		t.Fatal(err)
	}

	doc, err := s.buildScreen("", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, д := range doc.Screens[0].Children {
		if д.Title != "Сёма · Вадим" {
			continue
		}
		// числа тут быть не должно: цифра рядом с названием чата
		// читается как «столько непрочитанных сообщений»
		if д.Badge != "" {
			t.Errorf("на разговоре стоит число %q — его читают как непрочитанные сообщения", д.Badge)
		}
		if д.Level == "" {
			t.Error("на разговоре с Сёмой нет точки уровня — не видно, где ждёт запись")
		}
		// сколько именно ждёт — сказано словами там, где есть место
		doc, err := s.buildScreen("chat:"+strconv.FormatInt(сёма, 10), "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(doc.Subheading, "ждёт решения") {
			t.Errorf("в подписи разговора не сказано, сколько ждёт: %q", doc.Subheading)
		}
		return
	}
	t.Fatal("разговора с Сёмой нет в меню")
}

// Счётчик рядом со словом «Чаты» — это непрочитанные разговоры, а не
// число ждущих записей: в списке переписки цифру читают именно так.
func TestChatCounterIsAboutUnread(t *testing.T) {
	s, st := testServer(t)
	conversation, _ := st.CreateChat("Telegram · Вадим")
	сёма, _ := st.CreateThread(conversation, store.ChatKindSema, 0, "Сёма · Вадим")
	_ = st.AddChatMsg(сёма, "assistant", "смотрю")
	s.markChatRead(conversation)
	s.markChatRead(сёма)

	// три записи ждут решения, но непрочитанных разговоров нет
	for _, k := range []string{"a", "b", "c"} {
		if _, err := st.PutNotification(&store.Notification{
			Key: k, Source: "core", Level: "warn", Title: "ждёт " + k, State: store.NotifyOpen,
		}); err != nil {
			t.Fatal(err)
		}
	}

	doc, err := s.buildScreen("", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	чаты := doc.Screens[0]
	if чаты.Badge != "" {
		t.Errorf("счётчик «Чатов» показывает %q, хотя непрочитанных разговоров нет", чаты.Badge)
	}
	if чаты.Level == "" {
		t.Error("ждущие записи есть, а точки уровня на «Чатах» нет")
	}
}

// Запись, которая ждёт решения, приходит карточкой в разговор с Сёмой;
// в обычном чате её нет — там говорят с человеком, а не с Сёмой.
func TestWaitingArriveInSemaConversation(t *testing.T) {
	s, st := testServer(t)
	conversation, _ := st.CreateChat("Telegram · Мила")
	сёма, _ := st.CreateThread(conversation, store.ChatKindSema, 0, "Сёма · Мила")
	_ = st.AddChatMsg(сёма, "assistant", "смотрю")
	_ = st.AddChatMsg(conversation, "peer", "привет")

	_, err := st.PutNotification(&store.Notification{
		Key: "servers:daily", Source: "module:watch", Level: "warn",
		Title: "Требует внимания: srv-vpn", State: store.NotifyOpen,
	})
	if err != nil {
		t.Fatal(err)
	}

	has := func(chatID int64, text string) bool {
		doc, err := s.buildScreen("chat:"+strconv.FormatInt(chatID, 10), "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		var walk func([]Component) bool
		walk = func(cs []Component) bool {
			for _, c := range cs {
				if strings.Contains(c.Title, text) || strings.Contains(c.Subtitle, text) {
					return true
				}
				if walk(c.Children) {
					return true
				}
			}
			return false
		}
		return walk(doc.Components)
	}

	if !has(сёма, "Требует внимания") {
		t.Error("запись не пришла в разговор с Сёмой")
	}
	if has(conversation, "Требует внимания") {
		t.Error("запись попала в чат с человеком — там говорят не с Сёмой")
	}
}

// Спокойные записи (ok) не занимают ленту карточками: четыре ночные
// смены — одна свёрнутая строка.
func TestCalmRecordsCollapse(t *testing.T) {
	s, st := testServer(t)
	conversation, _ := st.CreateChat("Telegram · Вадим")
	сёма, _ := st.CreateThread(conversation, store.ChatKindSema, 0, "Сёма · Вадим")
	_ = st.AddChatMsg(сёма, "assistant", "готово")

	for _, host := range []string{"Microtik", "srv-vpn", "WireGuard", "srv-bots"} {
		if _, err := st.PutNotification(&store.Notification{
			Key: "night:" + host, Source: "core", Level: "ok",
			Title: host + " · ночная смена", State: store.NotifyOpen,
		}); err != nil {
			t.Fatal(err)
		}
	}

	doc, err := s.buildScreen("chat:"+strconv.FormatInt(сёма, 10), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var карточек, свёрнутых int
	for _, c := range doc.Components {
		switch {
		case c.ID == "спокойные-записи":
			свёрнутых++
			if !c.Collapsed {
				t.Error("спокойные записи развёрнуты — решать в них нечего")
			}
			if len(c.Children) != 4 {
				t.Errorf("в свёрнутой строке %d записей вместо четырёх", len(c.Children))
			}
		case strings.Contains(c.Title, "ночная смена"):
			карточек++
		}
	}
	if свёрнутых != 1 {
		t.Errorf("ожидалась одна свёрнутая строка, а вышло %d", свёрнутых)
	}
	if карточек > 0 {
		t.Errorf("%d спокойных записей заняли ленту карточками", карточек)
	}
}

// На свежей установке чатов ещё нет: «Чаты» — обычная кнопка, и она
// обязана вести на экран разговора. Признак «без своей строки» гасит
// адрес только у узла с детьми, иначе кнопка без детей стала бы мёртвой.
func TestChatsOpenWithoutASingleConversation(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	чаты := doc.Screens[0]
	if len(чаты.Children) != 0 {
		t.Fatalf("на пустой установке у «Чатов» есть дети: %+v", чаты.Children)
	}
	п := toMenuItem(чаты, "files", "")
	if п.IsNode {
		t.Fatal("без разговоров «Чаты» не должны быть раскрывающимся узлом")
	}
	if п.Addr != "ask" {
		t.Errorf("кнопка «Чаты» без разговоров никуда не ведёт: адрес %q", п.Addr)
	}
}

// Ветка «Сёма» заводится под каждым разговором. Записи, ждущие решения,
// приходят только в главный разговор — самый старый, — а не в каждую
// ветку Сёмы: иначе одна и та же запись висела бы в пяти местах, и
// «главным» оказывался бы случайный разговор.
func TestRecordsOnlyInMainConversation(t *testing.T) {
	s, st := testServer(t)
	я, _ := st.CreateChat("Telegram · Вадим")
	главный, _ := st.CreateThread(я, store.ChatKindSema, 0, "Сёма · Вадим")
	мила, _ := st.CreateChat("Telegram · Мила")
	вторая, _ := st.CreateThread(мила, store.ChatKindSema, 0, "Сёма · Мила")
	_ = st.AddChatMsg(главный, "assistant", "готово")
	_ = st.AddChatMsg(вторая, "assistant", "смотрю")
	if _, err := st.PutNotification(&store.Notification{
		Key: "servers:daily", Source: "module:watch", Level: "warn",
		Title: "Требует внимания: srv-vpn", State: store.NotifyOpen,
	}); err != nil {
		t.Fatal(err)
	}

	has := func(chatID int64) bool {
		doc, err := s.buildScreen("chat:"+strconv.FormatInt(chatID, 10), "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range doc.Components {
			if strings.Contains(c.Title+c.Subtitle, "Требует внимания") {
				return true
			}
		}
		return false
	}
	if !has(главный) {
		t.Error("запись не пришла в главный разговор с Сёмой")
	}
	if has(вторая) {
		t.Error("запись пришла и в ветку Сёмы под перепиской с Марусей — она не главная")
	}

	// пустой адрес открывает главный, а не ту ветку, что свежее
	doc, err := s.buildScreen("", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Active != "chat:"+strconv.FormatInt(главный, 10) {
		t.Errorf("открылся не главный разговор, а %q", doc.Active)
	}
}

// Меню по макету 16 сентября 2026: у разделов цветные значки, у групп
// мессенджеров — плитки, у разговора с Сёмой — инициалы, блоки разделены
// чертой. Цвет значка — оттенок раздела, а не состояние.
func TestMenuMatchesMockup(t *testing.T) {
	s, st := testServer(t)
	я, _ := st.CreateChat("Telegram · Вадим")
	главный, _ := st.CreateThread(я, store.ChatKindSema, 0, "Сёма · Вадим")
	_ = st.AddChatMsg(главный, "assistant", "готово")
	семья, _ := st.CreateChat("MAX · Семья")
	_ = st.AddChatMsg(семья, "peer", "ужин")

	doc, err := s.buildScreen("", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	по := map[string]Screen{}
	for _, э := range doc.Screens {
		по[э.ID] = э
	}
	for id, оттенок := range map[string]string{
		"ask": "blue", "inbox": "blue", "tasks": "blue", "scripts": "violet",
		"modules": "green", "memory": "orange", "health": "red",
	} {
		if по[id].Tint != оттенок {
			t.Errorf("у раздела %s оттенок %q, а по макету %q", id, по[id].Tint, оттенок)
		}
	}
	if по["modules"].Icon != "cube" {
		t.Errorf("у «Модулей» значок %q, а по макету куб", по["modules"].Icon)
	}
	for _, id := range []string{notifyScreen, "files", "settings"} {
		if !по[id].Divider {
			t.Errorf("над «%s» нет черты между блоками", по[id].Title)
		}
	}
	// «Уведомления» и «Почта» — один блок входящего: черты между ними нет
	if по["inbox"].Divider {
		t.Error("между «Уведомлениями» и «Почтой» черта — это один блок")
	}
	if по["ask"].GroupIcon == "" {
		t.Error("у подписи «Переписка» нет значка")
	}

	var сёма, telegram, max *Screen
	for i, ребёнок := range по["ask"].Children {
		switch ребёнок.Title {
		case "Сёма · Вадим":
			сёма = &по["ask"].Children[i]
		case "Telegram":
			telegram = &по["ask"].Children[i]
		case "MAX":
			max = &по["ask"].Children[i]
		}
	}
	if сёма == nil || сёма.Initials != "СВ" {
		t.Errorf("у разговора с Сёмой нет инициалов «СВ»: %+v", сёма)
	}
	if telegram == nil || !telegram.Tile || telegram.Icon != "logo.telegram" {
		t.Errorf("группа Telegram не плиткой с логотипом: %+v", telegram)
	}
	if max == nil || !max.Tile || max.Icon != "logo.max" {
		t.Errorf("группа MAX не плиткой с логотипом: %+v", max)
	}

	// один ящик — у «Почты» нет строки-двойника «Обзор»
	if len(по["inbox"].Children) == 1 && !по["inbox"].NoSelf {
		t.Error("у «Почты» с одним ящиком рисуется «Обзор» — двойник единственного ящика")
	}
}

// Инициалы — две первые буквы слов, разделители словом не считаются.
func TestInitials(t *testing.T) {
	for вход, want := range map[string]string{
		"Сёма · Вадим": "СВ", "Про отпуск": "ПО", "Мила": "М", "": "",
	} {
		if got := initials(вход); got != want {
			t.Errorf("инициалы(%q) = %q, ждали %q", вход, got, want)
		}
	}
}

// Пункт «Уведомления» в меню постоянный: не пропадает, когда ждать нечему,
// и не меняет название. «Ждут вас» и «Уведомления» сменяли друг друга, а
// меню от перехода меняться не должно.
func TestNotificationsItemIsPermanent(t *testing.T) {
	s, st := testServer(t)
	найти := func() *Screen {
		doc, err := s.buildScreen("", "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := range doc.Screens {
			if doc.Screens[i].ID == notifyScreen {
				return &doc.Screens[i]
			}
		}
		return nil
	}

	// ждать нечему — пункт на месте, без счётчика
	empty := найти()
	if empty == nil {
		t.Fatal("пункта «Уведомления» нет в меню, когда ждать нечему")
	}
	if empty.Badge != "" {
		t.Errorf("ждать нечему, а у пункта счётчик %q", empty.Badge)
	}

	// запись ждёт решения — тот же пункт, то же название, со счётчиком
	if _, err := st.PutNotification(&store.Notification{
		Key: "servers:daily", Source: "module:watch", Level: "warn",
		Title: "Требует внимания", State: store.NotifyOpen,
	}); err != nil {
		t.Fatal(err)
	}
	ждёт := найти()
	if ждёт == nil || ждёт.Title != empty.Title {
		t.Errorf("название пункта сменилось: было %q, стало %+v", empty.Title, ждёт)
	}
	if ждёт != nil && ждёт.Badge == "" {
		t.Error("запись ждёт решения, а счётчика у пункта нет")
	}
}
