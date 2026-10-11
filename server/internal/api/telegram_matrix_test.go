package api

// Права собеседников Telegram матрицей «чат × право» (telegram_chats.go):
// клетка переключает право тем же действием, что был тумблер, и движок видит
// новое значение; права, которых у такого чата не бывает, — клетки «na»,
// которые не переключаются ни нажатием, ни прямым вызовом действия; тема группы
// показывает действующее право группы, пока не переопределила его; поиск, отбор
// и порции идут на сервере. Ни один тест в Telegram не ходит: история в
// фикстуре выключена (telegramWithChannels), а чаты с темами не читаются.

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// tgMatrixOf — матрица прав экрана «Собеседники».
func tgMatrixOf(t *testing.T, doc *Document) *Component {
	t.Helper()
	m := findByID(doc.Components, "tg-chats")
	if m == nil || m.Type != "matrix" {
		t.Fatalf("на экране нет матрицы прав: %+v", m)
	}
	return m
}

// tgCell — клетка строки в колонке, названной словом из шапки.
func tgCell(t *testing.T, m *Component, rowID, column string) Cell {
	t.Helper()
	row := findByID(m.Children, rowID)
	if row == nil {
		t.Fatalf("в матрице нет строки %q", rowID)
	}
	for i, col := range m.Columns {
		if col == column {
			if i >= len(row.Cells) {
				t.Fatalf("у строки %q нет клетки в колонке «%s»", rowID, column)
			}
			return row.Cells[i]
		}
	}
	t.Fatalf("в матрице нет колонки «%s»: %v", column, m.Columns)
	return Cell{}
}

// tgPress — нажать клетку так, как нажимает браузер: то же действие, та же цель.
func tgPress(t *testing.T, s *Server, c Cell) error {
	t.Helper()
	if c.Action.Action == "" {
		t.Fatalf("клетка не нажимается: %+v", c)
	}
	_, err := s.execute(actionRequest{Action: c.Action.Action, Target: c.Action.Target})
	return err
}

// tgMatrixScreen — экран «Собеседники» заново: после нажатия человек видит
// пересобранный экран.
func tgMatrixScreen(t *testing.T, s *Server, filter, search string, limit int) (*Document, *Component) {
	t.Helper()
	doc, err := s.buildScreen(tgScreen, filter, search, limit)
	if err != nil {
		t.Fatal(err)
	}
	return doc, tgMatrixOf(t, doc)
}

// telegramMatrixFix — личный чат, группа, группа с тремя темами и два канала
// (первый — владельца, второй — подписчика).
func telegramMatrixFix(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s, st := telegramWithChannels(t, 2)
	forum := &store.TGChat{Account: telegramAccount, ID: 3, Title: "Клуб", Kind: "group", Class: store.ClassSensitive, Forum: true}
	if err := st.SaveTGChat(forum); err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"Общий", "Разговоры", "Новости"} {
		if err := st.SaveTGTopic(telegramAccount, 3, i+1, title); err != nil {
			t.Fatal(err)
		}
	}
	return s, st
}

// readRight — право чата, как его видит движок после нажатия.
func readRight(c *store.TGChat, right string) bool {
	return map[string]bool{"collect": c.Collect, "files": c.Files, "reply": c.Reply,
		"send": c.Send, "admin": c.Admin, "blocked": c.Blocked}[right]
}

// Клетка каждого права у личного чата, группы и канала переключает его, и
// экран после нажатия показывает то же, что видит движок.
func TestTelegramMatrixCellSwitchesRight(t *testing.T) {
	s, st := telegramMatrixFix(t)
	columns := []struct{ column, right string }{
		{"Читать", "collect"}, {"Файлы", "files"}, {"Отвечать", "reply"},
		{"Писать первым", "send"}, {"Админ", "admin"}, {"Не трогать", "blocked"},
	}
	// у каждого вида — какие клетки обязаны нажиматься
	chats := []struct {
		id    int64
		kind  string
		works map[string]bool
	}{
		{1, "личный", map[string]bool{"collect": true, "files": true, "reply": true, "send": true, "admin": true, "blocked": true}},
		{2, "группа", map[string]bool{"collect": true, "files": true, "reply": true, "send": true, "blocked": true}},
		{101, "канал", map[string]bool{"collect": true, "files": true, "send": true, "blocked": true}},
	}
	for _, c := range chats {
		rowID := fmt.Sprintf("tg-%d", c.id)
		for _, col := range columns {
			if col.right == "blocked" {
				continue // блокировка снимает остальное — проверяется ниже отдельно
			}
			_, m := tgMatrixScreen(t, s, "", "", 0)
			cell := tgCell(t, m, rowID, col.column)
			if !c.works[col.right] {
				if cell.State != "na" || cell.Action.Action != "" || cell.Title == "" {
					t.Errorf("%s: клетка «%s» должна быть «na» с причиной, а она %+v", c.kind, col.column, cell)
				}
				continue
			}
			if cell.State != "off" {
				t.Fatalf("%s: клетка «%s» до нажатия %q", c.kind, col.column, cell.State)
			}
			if err := tgPress(t, s, cell); err != nil {
				t.Fatalf("%s: «%s» не включилось: %v", c.kind, col.column, err)
			}
			got, _ := st.TGChat(telegramAccount, c.id)
			if !readRight(got, col.right) {
				t.Errorf("%s: нажали «%s», а движок видит выключенным", c.kind, col.column)
			}
			if _, m = tgMatrixScreen(t, s, "", "", 0); tgCell(t, m, rowID, col.column).State != "on" {
				t.Errorf("%s: экран после нажатия «%s» не показывает «включено»", c.kind, col.column)
			}
			// нажатие второй раз возвращает прежнее
			if err := tgPress(t, s, tgCell(t, m, rowID, col.column)); err != nil {
				t.Fatal(err)
			}
			if got, _ = st.TGChat(telegramAccount, c.id); readRight(got, col.right) {
				t.Errorf("%s: второе нажатие «%s» не выключило", c.kind, col.column)
			}
		}
		// блокировка: снимает остальные права, иначе они однажды сработали бы
		_, m := tgMatrixScreen(t, s, "", "", 0)
		if err := tgPress(t, s, tgCell(t, m, rowID, "Читать")); err != nil {
			t.Fatal(err)
		}
		_, m = tgMatrixScreen(t, s, "", "", 0)
		if err := tgPress(t, s, tgCell(t, m, rowID, "Не трогать")); err != nil {
			t.Fatal(err)
		}
		got, _ := st.TGChat(telegramAccount, c.id)
		if !got.Blocked || got.Collect {
			t.Errorf("%s: блокировка: blocked=%v collect=%v (ждали да и нет)", c.kind, got.Blocked, got.Collect)
		}
		if _, m = tgMatrixScreen(t, s, "", "", 0); findByID(m.Children, rowID).Level != LevelAlert {
			t.Errorf("%s: заблокированный чат не помечен уровнем «тревога»", c.kind)
		}
	}
}

// Права, которых у такого чата не бывает, не переключаются и прямым вызовом
// действия: клетка «na» — не единственная защита, отказывает сам обработчик.
func TestTelegramMatrixImpossibleRightsRefuseDirectCall(t *testing.T) {
	s, st := telegramMatrixFix(t)
	cases := []struct {
		name, action, target string
	}{
		{"админ у группы", "tg_chat_right", telegramTarget(2) + "~admin"},
		{"админ у канала", "tg_chat_right", telegramTarget(101) + "~admin"},
		{"отвечать каналу", "tg_chat_right", telegramTarget(101) + "~reply"},
		{"публиковать подписчику", "tg_chat_right", telegramTarget(102) + "~send"},
		{"напоминать каналу", "tg_wait_toggle", telegramTarget(101)},
		{"выдуманное право", "tg_chat_right", telegramTarget(1) + "~wait"},
		{"админ у темы", "tg_topic_right", telegramTarget(3) + "/2~admin"},
	}
	for _, c := range cases {
		if _, err := s.execute(actionRequest{Action: c.action, Target: c.target}); err == nil {
			t.Errorf("%s: прямой вызов прошёл", c.name)
		}
	}
	for _, id := range []int64{2, 101, 102} {
		got, _ := st.TGChat(telegramAccount, id)
		if got.Admin || got.Reply || got.Send {
			t.Errorf("чат %d: отказ что-то записал: %+v", id, got)
		}
	}
	if s.eng.TGWaitIsOff(telegramAccount, 101) {
		t.Error("отказанное напоминание каналу записалось")
	}
	// клетки «na» нет у канала в колонках, где права не бывает, — и у темы тоже
	_, m := tgMatrixScreen(t, s, "", "", 0)
	for _, col := range []string{"Админ", "Напоминать"} {
		if c := tgCell(t, m, "tg-3-2", col); c.State != "na" || c.Action.Action != "" || c.Select != nil {
			t.Errorf("у темы клетка «%s» нажимается: %+v", col, c)
		}
	}
}

// «Напоминать» и класс — те же действия, что были тумблером и выбором из трёх
// кнопок, но клеткой: тумблер молчания и список в клетке.
func TestTelegramMatrixWaitAndClassCells(t *testing.T) {
	s, st := telegramMatrixFix(t)
	_, m := tgMatrixScreen(t, s, "", "", 0)
	wait := tgCell(t, m, "tg-1", "Напоминать")
	if wait.State != "on" {
		t.Fatalf("напоминание у нового чата выключено: %+v", wait)
	}
	if err := tgPress(t, s, wait); err != nil {
		t.Fatal(err)
	}
	if !s.eng.TGWaitIsOff(telegramAccount, 1) {
		t.Error("нажали «Напоминать», а семафор не выключился")
	}
	if _, m = tgMatrixScreen(t, s, "", "", 0); tgCell(t, m, "tg-1", "Напоминать").State != "off" {
		t.Error("экран не показывает выключенное напоминание")
	}

	// класс — три пункта «···» строки с прежней целью «чат~класс»; на самой строке
	// капсула только когда класс не обычный для вида чата
	row := findByID(m.Children, "tg-1")
	if len(row.Menu) != 3 || !strings.Contains(row.Menu[1].Label, "сейчас") {
		t.Fatalf("меню класса личного чата: %+v", row.Menu)
	}
	for _, b := range row.Badges {
		if b.Text == store.ClassSensitive {
			t.Error("обычный для личного чата класс нарисован капсулой")
		}
	}
	for _, a := range []Action{row.Menu[2], row.Menu[0]} {
		if _, err := s.execute(actionRequest{Action: a.Action, Target: a.Target}); err != nil {
			t.Fatal(err)
		}
		_, want, _ := strings.Cut(a.Target, "~")
		if c, _ := st.TGChat(telegramAccount, 1); c.Class != want {
			t.Errorf("класс не стал %q: %q", want, c.Class)
		}
		if _, m = tgMatrixScreen(t, s, "", "", 0); !strings.Contains(findByID(m.Children, "tg-1").Menu[map[string]int{store.ClassPlain: 0, store.ClassSecret: 2}[want]].Label, "сейчас") {
			t.Errorf("после выбора %q в меню не отмечен текущий класс", want)
		}
	}
	// необычный класс виден капсулой: «обычное» у личного чата
	found := false
	for _, b := range findByID(m.Children, "tg-1").Badges {
		found = found || b.Text == store.ClassPlain
	}
	if !found {
		t.Error("необычный класс не виден капсулой на строке")
	}
	if _, err := s.execute(actionRequest{Action: "tg_chat_class", Target: telegramTarget(1) + "~выдуманный"}); err == nil {
		t.Error("выдуманный класс принят")
	}
	if _, err := s.execute(actionRequest{Action: "tg_chat_class", Target: telegramTarget(1)}); err == nil {
		t.Error("класс без значения принят")
	}
}

// Тема «как у группы» показывает действующее право группы (пунктиром); нажатие
// делает право своим, и тогда оно своё, а остальные остаются групповыми; «Как у
// группы» в «···» возвращает наследование.
func TestTelegramMatrixTopicInheritance(t *testing.T) {
	s, st := telegramMatrixFix(t)
	// у группы выданы «читать» и «файлы»: так у темы есть что наследовать
	for _, r := range []string{"collect", "files"} {
		if err := st.SetTGChatRight(telegramAccount, 3, r, true); err != nil {
			t.Fatal(err)
		}
	}
	// чтение группы включено напрямую в хранилище: через действие оно заводило бы
	// разговоры по темам и ходило в Telegram за списком тем
	_, m := tgMatrixScreen(t, s, "", "", 0)
	topic := "tg-3-2"
	if findByID(m.Children, topic) == nil {
		t.Fatalf("тем под группой нет: %+v", m.Children)
	}
	for _, col := range []string{"Читать", "Файлы"} {
		if c := tgCell(t, m, topic, col); c.State != "on" || !c.Inherited {
			t.Errorf("тема «как у группы»: клетка «%s» должна быть включённой и чужой, а она %+v", col, c)
		}
	}
	if c := tgCell(t, m, topic, "Отвечать"); c.State != "off" || !c.Inherited {
		t.Errorf("тема без права «отвечать»: %+v", c)
	}
	if row := findByID(m.Children, topic); len(row.Menu) != 0 || row.Caption != "как у группы" || row.Style != "nested" {
		t.Errorf("тема без своих прав: меню %v, подпись %q, стиль %q", row.Menu, row.Caption, row.Style)
	}

	// право группы меняется — тема, которая не переопределяла, следует за ним
	if err := st.SetTGChatRight(telegramAccount, 3, "reply", true); err != nil {
		t.Fatal(err)
	}
	_, m = tgMatrixScreen(t, s, "", "", 0)
	if c := tgCell(t, m, topic, "Отвечать"); c.State != "on" || !c.Inherited {
		t.Errorf("право группы не дошло до темы: %+v", c)
	}

	// нажатие делает право своим: действующее было «включено», станет «выключено»
	if err := tgPress(t, s, tgCell(t, m, topic, "Файлы")); err != nil {
		t.Fatal(err)
	}
	_, m = tgMatrixScreen(t, s, "", "", 0)
	files := tgCell(t, m, topic, "Файлы")
	if files.State != "off" || files.Inherited {
		t.Errorf("переопределённое право темы: %+v", files)
	}
	if c := tgCell(t, m, topic, "Читать"); !c.Inherited || c.State != "on" {
		t.Errorf("соседнее право темы перестало быть групповым: %+v", c)
	}
	group, _ := st.TGChat(telegramAccount, 3)
	if !group.Files {
		t.Error("переопределение темы переписало право самой группы")
	}
	if eff := s.eng.TGEffective(group, 2); eff.Files || !eff.Collect {
		t.Errorf("движок видит у темы files=%v collect=%v, ждали нет и да", eff.Files, eff.Collect)
	}
	// соседняя тема осталась как у группы
	if c := tgCell(t, m, "tg-3-3", "Файлы"); !c.Inherited || c.State != "on" {
		t.Errorf("переопределение одной темы задело другую: %+v", c)
	}
	row := findByID(m.Children, topic)
	if row.Caption != "свои права" || len(row.Menu) != 1 || row.Menu[0].Action != "tg_topic_inherit" {
		t.Fatalf("у темы со своими правами нет «Как у группы»: подпись %q, меню %+v", row.Caption, row.Menu)
	}
	if _, err := s.execute(actionRequest{Action: row.Menu[0].Action, Target: row.Menu[0].Target}); err != nil {
		t.Fatal(err)
	}
	_, m = tgMatrixScreen(t, s, "", "", 0)
	if c := tgCell(t, m, topic, "Файлы"); c.State != "on" || !c.Inherited {
		t.Errorf("после «Как у группы» право темы не вернулось к группе: %+v", c)
	}
	if row = findByID(m.Children, topic); len(row.Menu) != 0 {
		t.Error("«Как у группы» осталось у темы без своих прав")
	}
}

// Все пять прав темы переключаются клеткой, а право «не трогать» снимает
// остальные, как у чата.
func TestTelegramMatrixTopicCellsSwitchRights(t *testing.T) {
	s, _ := telegramMatrixFix(t)
	topic := "tg-3-2"
	chat, _ := s.eng.Store().TGChat(telegramAccount, 3)
	read := map[string]func() bool{}
	for right, col := range map[string]string{"collect": "Читать", "files": "Файлы", "reply": "Отвечать", "send": "Писать первым"} {
		right, col := right, col
		read[col] = func() bool {
			e := s.eng.TGEffective(chat, 2)
			return map[string]bool{"collect": e.Collect, "files": e.Files, "reply": e.Reply, "send": e.Send}[right]
		}
	}
	for col, got := range read {
		_, m := tgMatrixScreen(t, s, "", "", 0)
		if err := tgPress(t, s, tgCell(t, m, topic, col)); err != nil {
			t.Fatalf("тема, «%s»: %v", col, err)
		}
		if !got() {
			t.Errorf("тема: нажали «%s», а движок видит выключенным", col)
		}
		if _, m = tgMatrixScreen(t, s, "", "", 0); tgCell(t, m, topic, col).State != "on" || tgCell(t, m, topic, col).Inherited {
			t.Errorf("тема: экран после нажатия «%s»: %+v", col, tgCell(t, m, topic, col))
		}
	}
	_, m := tgMatrixScreen(t, s, "", "", 0)
	if err := tgPress(t, s, tgCell(t, m, topic, "Не трогать")); err != nil {
		t.Fatal(err)
	}
	eff := s.eng.TGEffective(chat, 2)
	if !eff.Blocked || eff.Collect || eff.Files || eff.Reply || eff.Send {
		t.Errorf("блокировка темы не сняла остальные права: %+v", eff)
	}
}

// Поиск и отбор идут на сервере и по всем чатам, а не по показанным.
func TestTelegramMatrixSearchAndFilters(t *testing.T) {
	s, st := telegramMatrixFix(t)
	for i := 0; i < 11; i++ {
		c := &store.TGChat{Account: telegramAccount, ID: int64(200 + i), Title: fmt.Sprintf("Собеседник %02d", i), Kind: "user", Class: store.ClassSensitive}
		if i == 0 {
			c.Name = "anna_k"
		}
		if err := st.SaveTGChat(c); err != nil {
			t.Fatal(err)
		}
	}
	// шестнадцать чатов: двенадцать личных, две группы, два канала
	if err := st.SetTGChatRight(telegramAccount, 200, "files", true); err != nil {
		t.Fatal(err)
	}

	doc, m := tgMatrixScreen(t, s, "", "", 0)
	if len(m.Children) < 16 {
		t.Fatalf("в матрице %d строк, ждали шестнадцать чатов и темы", len(m.Children))
	}
	titles := map[string]string{}
	for _, f := range doc.Filters {
		titles[f.ID] = f.Title
	}
	for id, want := range map[string]string{tgFilterAll: "Все · 16", tgFilterPeople: "Личные · 12", tgFilterGroups: "Группы · 2",
		tgFilterChannels: "Каналы · 2", tgFilterGranted: "С правами · 1"} {
		if titles[id] != want {
			t.Errorf("чип %s: %q, ждали %q", id, titles[id], want)
		}
	}
	if !doc.Searchable || doc.SearchHint != tgSearchHint {
		t.Errorf("верхнее поле не ищет чаты: Searchable=%v, подсказка %q", doc.Searchable, doc.SearchHint)
	}

	chats := func(filter, search string) []string {
		t.Helper()
		doc, err := s.buildScreen(tgScreen, filter, search, 0)
		if err != nil {
			t.Fatal(err)
		}
		m := findByID(doc.Components, "tg-chats")
		if m == nil || m.Type != "matrix" {
			return nil // ничего не нашлось: вместо матрицы объяснение
		}
		var out []string
		for _, r := range m.Children {
			if r.Style != "nested" {
				out = append(out, r.Subtitle)
			}
		}
		return out
	}
	for _, c := range []struct {
		filter, search string
		want           int
		first          string
	}{
		{tgFilterPeople, "", 12, ""},
		{tgFilterGroups, "", 2, ""},
		{tgFilterChannels, "", 2, ""},
		{tgFilterGranted, "", 1, "Собеседник 00"},
		{"", "anna_k", 1, "Собеседник 00"},
		{"", "ozon", 1, "Новости Ozon"},
		{"", "группа", 2, ""},
		{"", "канал", 2, ""},
		{"", "личный собеседник", 11, ""},
		{tgFilterChannels, "ozon", 1, "Новости Ozon"},
		{tgFilterPeople, "ozon", 0, ""},
	} {
		got := chats(c.filter, c.search)
		if len(got) != c.want || (c.first != "" && got[0] != c.first) {
			t.Errorf("отбор %q, поиск %q: нашлось %v, ждали %d", c.filter, c.search, got, c.want)
		}
	}

	// ничего не нашлось — объяснение, а не пустая таблица; общий поиск по разделам
	// над матрицей не встаёт: поле искало чаты
	doc, err := s.buildScreen(tgScreen, "", "такого-нет", 0)
	if err != nil {
		t.Fatal(err)
	}
	if findByID(doc.Components, "tg-chats").Type == "matrix" {
		t.Error("на пустой поиск осталась матрица")
	}
	if dump := dumpScreen(doc); !strings.Contains(dump, "Под отбор ничего не подошло") || strings.Contains(dump, "Нашлось в настройках") {
		t.Errorf("пустой поиск: %s", dump)
	}
	doc, _ = tgMatrixScreen(t, s, "", "anna_k", 0)
	if strings.Contains(dumpScreen(doc), "Нашлось в настройках") {
		t.Error("над матрицей встал общий поиск по настройкам")
	}
}

// Вес вкладки «Собеседники» на шестнадцати чатах: узлы и байты разметки, как она
// уходит в браузер. До переделки вкладка несла шестнадцать раскрытий по семь
// тумблеров — число до переделки записано в отчёте этого изменения.
func TestTelegramChatsTabWeight(t *testing.T) {
	s, st := telegramMatrixFix(t)
	for i := 0; i < 11; i++ {
		if err := st.SaveTGChat(&store.TGChat{Account: telegramAccount, ID: int64(200 + i),
			Title: fmt.Sprintf("Собеседник %02d", i), Kind: "user", Class: store.ClassSensitive}); err != nil {
			t.Fatal(err)
		}
	}
	w, took := modelsTimed(t, s, tgScreen)
	t.Logf("Telegram → Собеседники, 16 чатов (группа с тремя темами, два канала): %d узлов, %.1f КБ, сборка %s",
		w.Nodes, float64(w.Bytes)/1024, took)
	// старая вкладка на тех же данных: 1577 узлов и 116,7 КБ (экран и две ленивые
	// части, которые браузер забирал при раскрытии). Матрица несёт меньше узлов и
	// не больше байт: подсказки клеток коротки, а длинные пояснения прав стоят один
	// раз в справке под матрицей
	if w.Nodes > 1100 {
		t.Errorf("вкладка «Собеседники» несёт %d узлов, а ждали заметно меньше прежних 1577", w.Nodes)
	}
	if w.Bytes > 100*1024 {
		t.Errorf("вкладка «Собеседники» весит %.1f КБ, прежняя — 116,7 КБ", float64(w.Bytes)/1024)
	}
	// по сети разметка едет сжатой: тот же замер с gzip
	doc, _ := s.buildScreen(tgScreen, "", "", 0)
	markup, _ := web.Render(toScreen(doc))
	var packed bytes.Buffer
	zw := gzip.NewWriter(&packed)
	_, _ = zw.Write([]byte(markup))
	_ = zw.Close()
	t.Logf("то же сжатое: %.1f КБ", float64(packed.Len())/1024)
}
