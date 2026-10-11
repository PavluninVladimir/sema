package api

// Каналы на экране Telegram: те же строки матрицы прав, что у чатов (писать в
// канал Сёма не умеет — клеток «отвечать», «админ» и «напоминать» у канала нет,
// а «писать первым» — «публиковать»), класс списком в клетке, массовые кнопки с
// подтверждением, длинный список порциями.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// telegramAccount — номер, под которым в проверках заведены чаты: чат
// принадлежит учётке, а не висит сам по себе.
const telegramAccount = "+70000000001"

// telegramTarget — адрес чата в действиях экрана.
func telegramTarget(id int64) string { return tgTarget(telegramAccount, id) }

// tgDigits — номер учётки так, как он стоит в адресах экрана и действий.
var tgDigits = engine.NumberDigits(telegramAccount)

// telegramWithChannels — сервер после входа в Telegram: человек, группа и
// каналы в списке. Без ключей и аккаунта экран показывает мастер, а не
// вкладки.
func telegramWithChannels(t *testing.T, каналов int) (*Server, *store.Store) {
	t.Helper()
	s, st := testServer(t)
	if err := s.eng.SaveTGCreds("12345", "хэш"); err != nil {
		t.Fatal(err)
	}
	// истории ноль: включённое чтение заводит чат Сёмы и просит у Telegram
	// прошлую переписку, а тест в Telegram не ходит. С нулём разговор заводится
	// на месте, и никакого соединения не открывается
	if err := st.SetSetting("tg.history", "0"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTGAccount(&store.TGAccount{Phone: telegramAccount, Title: "личная",
		State: "ok", Enabled: true, Every: 1, Batch: 50}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*store.TGChat{
		{Account: telegramAccount, ID: 1, Title: "Митя", Kind: "user", Class: store.ClassSensitive},
		{Account: telegramAccount, ID: 2, Title: "Дом", Kind: "group", Class: store.ClassSensitive},
	} {
		if err := st.SaveTGChat(c); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= каналов; i++ {
		c := &store.TGChat{Account: telegramAccount, ID: int64(100 + i), Title: fmt.Sprintf("Канал номер %02d", i), Kind: "channel", Class: store.ClassPlain}
		if i == 1 {
			c.Title, c.Name = "Новости Ozon", "ozon_news"
			c.CanPost = true // владелец: тут можно публиковать
		}
		if err := st.SaveTGChat(c); err != nil {
			t.Fatal(err)
		}
	}
	return s, st
}

// findByID — компонент с таким ID в глубину.
func findByID(cs []Component, id string) *Component {
	for i := range cs {
		if cs[i].ID == id {
			return &cs[i]
		}
		if c := findByID(cs[i].Children, id); c != nil {
			return c
		}
	}
	return nil
}

func TestTelegramScreenChannelsInMatrixWithCommonSection(t *testing.T) {
	s, _ := telegramWithChannels(t, 2)
	doc, err := s.buildScreen(tgScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// чаты и каналы — строки одной матрицы, а не два списка: каналы отличает
	// капсула вида и клетки «na»
	m := tgMatrixOf(t, doc)
	if !strings.Contains(m.Title, "Собеседники · 4") {
		t.Errorf("заголовок матрицы: %q", m.Title)
	}
	for _, id := range []string{"tg-1", "tg-2", "tg-101", "tg-102"} {
		if findByID(m.Children, id) == nil {
			t.Errorf("в матрице нет строки %s", id)
		}
	}
	var заголовки []string
	for _, c := range doc.Components {
		if c.Type == "section" {
			заголовки = append(заголовки, c.Title)
		}
	}
	if все := strings.Join(заголовки, " | "); !strings.Contains(все, "Каналы · 2, читает 0") {
		t.Errorf("общего раздела каналов нет: %s", все)
	}
	// массовые кнопки — обе с подтверждением
	массово := findByID(doc.Components, "tg-channels-all")
	if массово == nil {
		t.Fatal("строки «Все каналы разом» нет")
	}
	var цели []string
	for _, a := range append(массово.Actions, массово.Menu...) {
		if a.Action == "tg_channels_all" {
			цели = append(цели, a.Target)
			if a.Confirm == "" || a.ConfirmOK == "" {
				t.Errorf("массовое действие %q без подтверждения", a.Target)
			}
		}
	}
	// у массовой кнопки в адресе стоит учётка: «читать все каналы» у
	// личной не должно включать чтение у рабочей
	if fmt.Sprint(цели) != fmt.Sprintf("[%s.on %s.off]", tgDigits, tgDigits) {
		t.Errorf("массовые действия: %v", цели)
	}
	// общий срок — подписан как общий и ведёт к полю на «Ответы и пределы»
	срок := findByID(doc.Components, "tg-lim-tg.post_days")
	if срок == nil || !strings.Contains(срок.Caption, "общее для всех каналов") ||
		len(срок.Actions) == 0 || срок.Actions[0].Action != "$goto" || !strings.HasSuffix(срок.Actions[0].Target, "~"+tgTabAnswer) {
		t.Errorf("срок «пост — разговор» не подписан общим или не ведёт к полю: %+v", срок)
	}
	// частей у экрана больше нет: матрица приезжает сразу, а поиск и порции идут
	// параметрами экрана
	if lazy := lazySections(doc.Components); len(lazy) != 0 {
		t.Errorf("на экране Telegram остались ленивые разделы: %v", lazy)
	}
}

func TestTGChannelRowGrantsWithoutReplies(t *testing.T) {
	s, st := telegramWithChannels(t, 2)
	doc, err := s.buildScreen(tgScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	m := tgMatrixOf(t, doc)
	row := findByID(m.Children, "tg-101")
	if row == nil {
		t.Fatal("строки канала нет")
	}
	var права []string
	for i, col := range m.Columns {
		cell := row.Cells[i]
		if cell.Action.Action == "tg_chat_right" {
			_, право, _ := strings.Cut(cell.Action.Target, "~")
			права = append(права, право)
		}
		if cell.State == "na" && cell.Title == "" {
			t.Errorf("у клетки «%s» нет причины", col)
		}
	}
	// порядок клеток — порядок колонок: читать, файлы, писать первым, не трогать
	if got := strings.Join(права, " "); got != "collect files send blocked" {
		t.Errorf("права канала: %q, ждали «collect files send blocked»", got)
	}
	for _, col := range []string{"Отвечать", "Админ", "Напоминать"} {
		if c := tgCell(t, m, "tg-101", col); c.State != "na" || c.Action.Action != "" {
			t.Errorf("у канала клетка «%s» нажимается: %+v", col, c)
		}
	}
	// класс — тремя пунктами «···» строки, действие и цель прежние («чат~класс»)
	var классы []string
	for _, a := range row.Menu {
		if a.Action == "tg_chat_class" {
			_, класс, _ := strings.Cut(a.Target, "~")
			классы = append(классы, класс)
		}
	}
	if got := strings.Join(классы, ","); got != store.ClassPlain+","+store.ClassSensitive+","+store.ClassSecret {
		t.Errorf("выбора класса у канала нет: %q", got)
	}
	if !strings.Contains(row.Menu[0].Label, "сейчас") {
		t.Errorf("текущий класс канала (обычное) не отмечен: %q", row.Menu[0].Label)
	}
	// у канала без права публиковать — не кнопка, а «na» с причиной
	if c := tgCell(t, m, "tg-102", "Писать первым"); c.State != "na" || !strings.Contains(c.Title, "нет права постить") {
		t.Errorf("подписчику предложена публикация: %+v", c)
	}
	// капсула вида и «вы админ»
	var бейджи []string
	for _, b := range row.Badges {
		бейджи = append(бейджи, b.Text)
	}
	if got := strings.Join(бейджи, ","); got != "канал,вы админ" {
		t.Errorf("капсулы канала: %q", got)
	}

	// поиск по нику идёт на сервере
	doc, _ = s.buildScreen(tgScreen, "", "ozon_news", 0)
	m = tgMatrixOf(t, doc)
	if len(m.Children) != 1 || m.Children[0].Subtitle != "Новости Ozon" {
		t.Errorf("поиск по нику: %+v", m.Children)
	}

	// действия: «отвечать» у канала — отказ, класс и массовое чтение — работают
	if _, err := s.execute(actionRequest{Action: "tg_chat_right", Target: telegramTarget(101) + "~reply"}); err == nil {
		t.Error("каналу выдали «отвечать», хотя писать в канал Сёма не умеет")
	}
	if _, err := s.execute(actionRequest{Action: "tg_chat_class", Target: telegramTarget(101) + "~" + store.ClassSensitive}); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.TGChat(telegramAccount, 101); c.Class != store.ClassSensitive {
		t.Errorf("класс канала не сохранился: %q", c.Class)
	}
	if _, err := s.execute(actionRequest{Action: "tg_chat_class", Target: telegramTarget(101) + "~выдуманный"}); err == nil {
		t.Error("выдуманный класс принят")
	}
	// заблокированный канал массовая кнопка не трогает
	if err := st.SetTGChatRight(telegramAccount, 102, "blocked", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute(actionRequest{Action: "tg_channels_all", Target: tgDigits + ".on"}); err != nil {
		t.Fatal(err)
	}
	c101, _ := st.TGChat(telegramAccount, 101)
	c102, _ := st.TGChat(telegramAccount, 102)
	c1, _ := st.TGChat(telegramAccount, 1)
	if !c101.Collect || c102.Collect || c1.Collect {
		t.Errorf("«читать все каналы»: 101=%v (ждали да), 102 заблокирован=%v (ждали нет), человек=%v (ждали нет)",
			c101.Collect, c102.Collect, c1.Collect)
	}
	if c101.Class != store.ClassSensitive {
		t.Errorf("массовое чтение затёрло класс, выбранный человеком: %q", c101.Class)
	}
	if _, err := s.execute(actionRequest{Action: "tg_channels_all", Target: tgDigits + ".off"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.TGChat(telegramAccount, 101); c.Collect {
		t.Error("«не читать ни один» не выключил чтение")
	}
}

// Список длиннее порции приезжает порциями: тридцать строк, «Показать ещё» с
// числами, а нажатие удваивает предел экрана.
func TestTGMatrixInBatches(t *testing.T) {
	s, _ := telegramWithChannels(t, 30)
	doc, err := s.buildScreen(tgScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	m := tgMatrixOf(t, doc)
	var ещё string
	for _, c := range doc.Components {
		if c.Type == "button" && len(c.Actions) > 0 && c.Actions[0].Action == "$more" {
			ещё = c.Actions[0].Label
		}
	}
	if len(m.Children) != tgChatsPage || !strings.Contains(ещё, "30 из 32") {
		t.Errorf("первая порция: %d строк, кнопка «ещё» говорит %q", len(m.Children), ещё)
	}
	// «Показать ещё» клиент просит у экрана с удвоенным пределом
	doc, _ = s.buildScreen(tgScreen, "", "", 100)
	if m = tgMatrixOf(t, doc); len(m.Children) != 32 {
		t.Errorf("после «ещё» строк %d, ждали 32", len(m.Children))
	}
	for _, c := range doc.Components {
		if c.Type == "button" && len(c.Actions) > 0 && c.Actions[0].Action == "$more" {
			t.Error("кнопка «ещё» осталась, когда показано всё")
		}
	}
}
