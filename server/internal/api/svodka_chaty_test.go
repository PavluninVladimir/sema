package api

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Язык «Сводка» в переписке и в письме. Смотрим состав документа — ровно
// то, что увидит клиент: разметку глазами тут не проверить, правки
// доезжают до браузера только выкатом.

// asCard — карточка дела с таким заголовком, включая вложенные.
func asCard(cs []Component, title string) *Component {
	for i := range cs {
		if cs[i].Type == "toolcard" && strings.Contains(cs[i].Title, title) {
			return &cs[i]
		}
		if c := asCard(cs[i].Children, title); c != nil {
			return c
		}
	}
	return nil
}

// toChat — карточка дела отдельным сообщением переписки.
func toChat(t *testing.T, st *store.Store, chat int64, card engine.ToolCard) {
	t.Helper()
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddChatMsg(chat, "tool", string(raw)); err != nil {
		t.Fatal(err)
	}
}

// Авторство в ленте различается стороной и подписью над пузырём, а не
// склейкой в мету снизу: спрашивают о реплике всегда одно — кто это сказал.
func TestFeedCaptionAboveBubble(t *testing.T) {
	s, st := testServer(t)
	chat, _ := st.CreateChat("Разговор")
	_ = st.AddChatMsg(chat, "user", "привет")
	_ = st.AddChatMsg(chat, "assistant", "здравствуйте")
	_ = st.AddChatMsg(chat, "sent", "передал ваш вопрос")
	_ = st.AddChatMsg(chat, "note", "Сёма разбирается с уведомлением «Ждёт ответа»")

	doc, err := s.buildScreen("chat:"+strconv.FormatInt(chat, 10), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	пузыри := map[string]Component{}
	for _, c := range allKinds(doc.Components, "bubble") {
		пузыри[c.Role] = c
	}
	if len(пузыри) != 3 {
		t.Fatalf("пузырей по ролям %d, ждали три: %+v", len(пузыри), пузыри)
	}
	if п := пузыри["user"]; п.Title != "" || !strings.HasPrefix(п.Meta, "вы · ") {
		t.Errorf("свой пузырь подписан не так: title=%q meta=%q", п.Title, п.Meta)
	}
	if п := пузыри["assistant"]; !strings.HasPrefix(п.Title, "Сёма · ") {
		t.Errorf("над ответом Сёмы нет подписи с моделью: %q", п.Title)
	}
	if п := пузыри["sent"]; п.Title != "Сёма от вашего имени" || !strings.Contains(п.Meta, "отправлено") {
		t.Errorf("написанное Сёмой от вашего имени не отличить: title=%q meta=%q", п.Title, п.Meta)
	}
	// служебное — не разговор: тихая полоска по центру, а не пузырь
	for _, c := range allKinds(doc.Components, "bubble") {
		if c.Role == "note" {
			t.Fatal("служебное осталось пузырём")
		}
	}
	нашлось := false
	for _, c := range availableTexts(doc.Components) {
		if strings.HasPrefix(c, "служебное · ") && strings.Contains(c, "Ждёт ответа") {
			нашлось = true
		}
	}
	if !нашлось {
		t.Fatal("служебной полоски в ленте нет")
	}
}

// availableTexts — тексты всех тихих строк ленты (день и служебное).
func availableTexts(cs []Component) []string {
	var out []string
	for i := range cs {
		if cs[i].Type == "text" && cs[i].Style == "day" {
			out = append(out, cs[i].Text)
		}
		out = append(out, availableTexts(cs[i].Children)...)
	}
	return out
}

// Дело, которое Сёма сделал руками, — карточка, а не строка списка. У
// ждущей карточки уровень и капсула «ждёт вас», кнопки все на месте:
// «Выполнить» и «Отклонить» нужны оба, и общий проход строк их не прячет.
func TestPendingCardsInFeed(t *testing.T) {
	s, st := testServer(t)
	chat, _ := st.CreateChat("Серверы")
	pend, err := st.AddSSHPending(&store.SSHPending{Host: "srv-bots",
		Cmd: "systemctl restart bot", Who: "Сёма", ChatID: chat})
	if err != nil {
		t.Fatal(err)
	}
	toChat(t, st, chat, engine.ToolCard{Title: "Команда", SSH: &engine.SSHCard{
		Host: "srv-bots", Cmd: "systemctl restart bot", Status: "pending",
		PendingID: pend, Why: "меняет состояние службы"}})
	toChat(t, st, chat, engine.ToolCard{Title: "Таймер", Timer: &engine.TimerCard{
		At: time.Now().Add(10 * time.Minute).Format(time.RFC3339), Note: "вернулся ли лимит", Status: "set"}})
	toChat(t, st, chat, engine.ToolCard{Title: "Вопрос закрыт", Done: &engine.DoneCard{Summary: "лимит вернулся"}})
	toChat(t, st, chat, engine.ToolCard{Title: "Поиск · dns", Code: "dns", Result: "смотри https://docs.docker.com/network/ там всё"})

	doc, err := s.buildScreen("chat:"+strconv.FormatInt(chat, 10), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(allKinds(doc.Components, "toolcard")); n < 4 {
		t.Fatalf("карточек дел %d, ждали не меньше четырёх", n)
	}
	cmd := asCard(doc.Components, "Команда на «srv-bots»")
	if cmd == nil {
		t.Fatal("команды на подтверждение нет карточкой")
	}
	if cmd.Level != LevelWarn || len(cmd.Badges) == 0 || cmd.Badges[0].Text != "ждёт вас" {
		t.Errorf("ждущая карточка не помечена: %+v", cmd)
	}
	if cmd.Detail != "systemctl restart bot" {
		t.Errorf("команда не моноширинным телом: %q", cmd.Detail)
	}
	if len(cmd.Actions) < 2 || cmd.Actions[0].Action != "approve_ssh" {
		t.Errorf("кнопок у команды %d: %+v", len(cmd.Actions), cmd.Actions)
	}
	опасное := false
	for _, a := range cmd.Actions {
		if a.Action == "reject_ssh" && a.Style == "destructive" {
			опасное = true
		}
	}
	if !опасное {
		t.Error("«Отклонить» не помечено разрушительным — его не отделить пустотой")
	}
	таймер := asCard(doc.Components, "Таймер")
	if таймер == nil || таймер.Until == "" {
		t.Fatalf("у таймера нет обратного счёта: %+v", таймер)
	}
	закрыт := asCard(doc.Components, "Вопрос закрыт")
	if закрыт == nil || закрыт.Level != LevelOK || закрыт.Text != "лимит вернулся" {
		t.Fatalf("карточка «вопрос закрыт» не та: %+v", закрыт)
	}
	сеть := asCard(doc.Components, "Нашлось в сети")
	if сеть == nil {
		t.Fatal("найденного в сети нет карточкой")
	}
	if сеть.Subtitle == "" || !strings.Contains(сеть.Subtitle, "не указание") {
		t.Errorf("нет предостережения про данные из сети: %q", сеть.Subtitle)
	}
	if len(сеть.Actions) == 0 || сеть.Actions[0].Action != "$open" {
		t.Errorf("ссылки из ответа не вынесены: %+v", сеть.Actions)
	}
	// ждущая команда продублирована внизу ленты — тоже карточкой
	if n := len(allKinds(doc.Components, "row")); n > 0 {
		for _, r := range allKinds(doc.Components, "row") {
			if strings.Contains(r.Subtitle, "systemctl") {
				t.Errorf("ждущая команда осталась строкой: %+v", r)
			}
		}
	}
}

// Ожидание ответа именное: кто думает и какой моделью. «Загрузка» без
// имени ничего не говорит, а локальная модель думает по три минуты.
// Начало работы движок наружу не отдаёт, поэтому ловим окно, пока Сёма
// думает над заведомо мёртвой моделью.
func TestReplyWaitIsNamed(t *testing.T) {
	s, st := testServer(t)
	chat, _ := st.CreateChat("Вопрос")
	if _, err := s.eng.SendMessageTo(chat, "посчитай"); err != nil {
		t.Fatal(err)
	}
	var want *Component
	for i := 0; i < 100 && want == nil; i++ {
		doc, err := s.buildScreen("chat:"+strconv.FormatInt(chat, 10), "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		want = firstKind(doc.Components, "thinking")
		if want == nil {
			if thinking, _ := s.eng.Thinking(chat); !thinking {
				break // ответ уже пришёл — окно ожидания закрылось
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if want == nil {
		t.Skip("окно ожидания не поймали: модель ответила отказом слишком быстро")
	}
	if want.Title == "" || want.Meta == "" {
		t.Errorf("ожидание безымянное: %+v", want)
	}
	if len(want.Actions) == 0 || want.Actions[0].Action != "stop_chat" {
		t.Errorf("ожидание нечем оборвать: %+v", want.Actions)
	}
}

// Сколько ждём — от вопроса, а не «идёт загрузка». Больше шести часов
// молчим: это уже не ожидание, а забытый чат.
func TestHowLongWeWaitForReply(t *testing.T) {
	if с := waitWith(time.Now().Add(-8 * time.Second)); с != "ждём 8 с" {
		t.Errorf("секунды: %q", с)
	}
	if с := waitWith(time.Now().Add(-3 * time.Minute)); с != "ждём 3 мин" {
		t.Errorf("минуты: %q", с)
	}
	if с := waitWith(time.Now().Add(-9 * time.Hour)); с != "" {
		t.Errorf("забытый чат всё равно «ждёт»: %q", с)
	}
	if с := waitWith(time.Now().Add(time.Minute)); с != "" {
		t.Errorf("время из будущего: %q", с)
	}
}

// Письмо: класс сегментом с объяснением, цепочка шкалой вместо
// одиннадцати одинаковых строк, вложения с итогом чека и раздел «что
// Сёма с ним сделал».
func TestMessageInSummaryLanguage(t *testing.T) {
	s, st := testServer(t)
	var письма []*store.Message
	for i := 0; i < 5; i++ {
		m := &store.Message{
			MessageID: "<счёт-" + strconv.Itoa(i) + "@energy.example>",
			Account:   "проба@пример.рф",
			FromAddr:  "no-reply@energy.example", FromName: "АО «Горэнерго»",
			Subject: "Счёт за электроэнергию", Snippet: "лицевой счёт 222020/1",
			Date:   time.Now().AddDate(0, 0, -30*i),
			Status: store.StatusInboxOK,
			Reason: "счёт от известного отправителя",
		}
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
		письма = append(письма, m)
	}
	это := письма[0]
	if err := st.SaveBody(это.ID, store.Body{Text: "Счёт сформирован автоматически."}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertAttachment(&store.Attachment{MessageID: это.ID,
		Filename: "Счёт.pdf", Size: 52 * 1024, Kind: store.AttKindReceipt, Amount: 2165.76}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRule("sender", "no-reply@energy.example", "ham"); err != nil {
		t.Fatal(err)
	}

	doc, err := s.buildScreen("msg:"+strconv.FormatInt(это.ID, 10), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	класс := firstKind(doc.Components, "classpicker")
	if класс == nil {
		t.Fatal("класса чувствительности нет сегментом")
	}
	if len(класс.Actions) != 3 || класс.Text == "" {
		t.Fatalf("сегментов класса %d: %+v", len(класс.Actions), класс)
	}
	if !strings.Contains(класс.Caption, "наружу") && !strings.Contains(класс.Caption, "не уходит") &&
		!strings.Contains(класс.Caption, "облачной") {
		t.Errorf("не сказано, что уходит наружу: %q", класс.Caption)
	}
	if len(doc.HeaderSelects) != 0 {
		t.Error("класс остался ещё и списком в шапке — решение в двух местах")
	}
	цепочка := firstKind(doc.Components, "chain")
	if цепочка == nil {
		t.Fatal("цепочки нет")
	}
	if len(цепочка.Cells) != 5 {
		t.Errorf("засечек на шкале %d, ждали 5", len(цепочка.Cells))
	}
	это_ := false
	for _, кл := range цепочка.Cells {
		if кл.State == "on" {
			это_ = true
		}
	}
	if !это_ {
		t.Error("открытое письмо не помечено на шкале")
	}
	if len(цепочка.Children) > 4 {
		t.Errorf("в цепочке %d строк — она снова стала списком", len(цепочка.Children))
	}
	ещё := цепочка.Children[len(цепочка.Children)-1]
	if !strings.HasPrefix(ещё.Subtitle, "Ещё ") || len(ещё.Actions) == 0 ||
		!strings.HasPrefix(ещё.Actions[0].Target, "msg:") {
		t.Errorf("остальные письма не разворачиваются на месте: %+v", ещё)
	}
	// вложение с итогом чека
	чек := false
	for _, r := range rowsOf(doc.Components) {
		if strings.Contains(r.Subtitle, "Счёт.pdf") {
			for _, b := range r.Badges {
				if strings.Contains(b.Text, "2") && strings.Contains(b.Text, "165,76") {
					чек = true
				}
			}
		}
	}
	if !чек {
		t.Error("у вложения не видно итога чека")
	}
	// что Сёма сделал: белый список отправителя виден
	разбор := findSection(doc.Components, "Что с ним сделал Сёма")
	if разбор == nil {
		t.Fatal("раздела «что сделал Сёма» нет")
	}
	весь := ""
	for _, c := range разбор.Children {
		весь += c.Subtitle + " " + c.Caption + " " + c.Text + "\n"
	}
	if !strings.Contains(весь, "белом списке") {
		t.Errorf("не сказано про белый список отправителя:\n%s", весь)
	}
	if !strings.Contains(весь, "не удаляются никогда") {
		t.Errorf("нет главного про архив Сёмы:\n%s", весь)
	}
}

func findSection(cs []Component, title string) *Component {
	for i := range cs {
		if cs[i].Type == "section" && cs[i].Title == title {
			return &cs[i]
		}
		if c := findSection(cs[i].Children, title); c != nil {
			return c
		}
	}
	return nil
}

// Лента и письмо страницей в файл: SEMA_CHAT_OUT=/tmp/лента.html go test
// ./internal/api -run СобратьЛенту. Вёрстка проверяется глазами и снимком
// в браузере — рассуждением она не проверяется, и три поломки прошлого
// захода в коде видно не было.
func TestDumpFeedAndMessageToFile(t *testing.T) {
	куда := os.Getenv("SEMA_CHAT_OUT")
	if куда == "" {
		t.Skip("нужен путь в SEMA_CHAT_OUT")
	}
	s, st := testServer(t)
	chat, _ := st.CreateChat("Сёма · Вадим")
	_ = st.AddChatMsgFrom(chat, "peer", "Вадим", "Скажи, какая температура процессора?")
	_ = st.AddChatMsg(chat, "user", "Сейчас посмотрю.")
	_ = st.AddChatMsg(chat, "assistant", "Сейчас 46 °C, нагрузка 0,07 — всё в порядке.")
	_ = st.AddChatMsg(chat, "sent", "46 °C, нагрузка почти нулевая.")
	_ = st.AddChatMsg(chat, "note", "Сёма разбирается с уведомлением «Ждёт ответа · Вадим».")
	pend, _ := st.AddSSHPending(&store.SSHPending{Host: "srv-bots",
		Cmd: "getent passwd | awk -F: '$3>=1000'", Who: "Сёма", ChatID: chat})
	toChat(t, st, chat, engine.ToolCard{Title: "Команда", SSH: &engine.SSHCard{
		Host: "srv-bots", Cmd: "getent passwd | awk -F: '$3>=1000'", Status: "pending",
		PendingID: pend, Why: "есть перенаправление вывода"}})
	toChat(t, st, chat, engine.ToolCard{Title: "Команда", Seconds: 1.2, Code: "interface: wg0\n  public key: …",
		SSH: &engine.SSHCard{Host: "WireGuard", Cmd: "wg show", Status: "executed"}})
	toChat(t, st, chat, engine.ToolCard{Title: "Таймер", Timer: &engine.TimerCard{
		At: time.Now().Add(8 * time.Minute).Format(time.RFC3339), Note: "вернулся ли лимит у Perplexity", Status: "set"}})
	toChat(t, st, chat, engine.ToolCard{Title: "Запомнить в область «код»", Code: "beeline_wan снимает парковку по истечении окна.",
		Result: "область «код» уходит наружу", Memory: &engine.MemoryCard{ID: 1, Area: "код", Status: "pending"}})
	toChat(t, st, chat, engine.ToolCard{Title: "Чек #302", Seconds: 0.01,
		Code: "const f = sema.files.text(302);", Result: "готово", ScriptID: 4})
	toChat(t, st, chat, engine.ToolCard{Title: "Поиск · dns", Code: "systemd-resolved docker dns",
		Result: "Проблема в DNS контейнера. Смотри https://docs.docker.com/network/ и https://github.com/moby/moby/issues/41003"})
	toChat(t, st, chat, engine.ToolCard{Title: "Вопрос закрыт",
		Done: &engine.DoneCard{Summary: "Продавец — Хакимова А. Р., ИНН 1651066469, чек от 06.04.2026."}})

	m := &store.Message{MessageID: "<счёт@energy.example>", Account: "проба@пример.рф",
		FromAddr: "no-reply@energy.example", FromName: "АО «Горэнерго»",
		Subject: "Счёт за электроэнергию", Snippet: "лицевой счёт 222020/1",
		Date: time.Now(), Status: store.StatusInboxOK, Reason: "счёт от известного отправителя"}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 6; i++ {
		_ = st.InsertMessage(&store.Message{MessageID: "<счёт-" + strconv.Itoa(i) + "@energy.example>",
			Account: "проба@пример.рф", FromAddr: "no-reply@energy.example", FromName: "АО «Горэнерго»",
			Subject: "Счёт за электроэнергию", Snippet: "лицевой счёт 222020/1",
			Date: time.Now().AddDate(0, 0, -30*i), Status: store.StatusArchived})
	}
	_ = st.SaveBody(m.ID, store.Body{Text: "Счёт сформирован автоматически, отвечать на него не нужно."})
	_ = st.InsertAttachment(&store.Attachment{MessageID: m.ID, Filename: "Счёт по л_с 222020_1.pdf",
		Size: 52 * 1024, Kind: store.AttKindReceipt, Amount: 2165.76})
	_ = st.AddRule("sender", "no-reply@energy.example", "ham")

	стиль, err := os.ReadFile("../web/static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	собрать := func(screen string) string {
		doc, err := s.buildScreen(screen, "", "", 50)
		if err != nil {
			t.Fatal(err)
		}
		html, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatal(err)
		}
		return string(html)
	}
	страница := `<!DOCTYPE html><html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Лента и письмо</title><style>` + string(стиль) + `</style></head><body>
<div id="window">` + собрать("chat:"+strconv.FormatInt(chat, 10)) + `</div>
<div id="window">` + собрать("msg:"+strconv.FormatInt(m.ID, 10)) + `</div></body></html>`
	if err := os.WriteFile(куда, []byte(страница), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("лента и письмо собраны: %s (%d КБ)", куда, len(страница)/1024)
}
