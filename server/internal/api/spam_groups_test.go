package api

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Сорок одинаковых писем от одного отправителя — одна строка, а не сорок.
// 10 сентября 2026 на живой Сёме в списке лежало 89 писем, и 41 из них было
// одним и тем же письмом от Ozon: человек листал сорок одинаковых строк и
// нажимал «Не спам» сорок раз.
func TestSpamGroupedBySender(t *testing.T) {
	s, st := testServer(t)
	добавить := func(id, тема, addr, name string, когда time.Time, уверен float64, почему string) {
		t.Helper()
		m := &store.Message{MessageID: id, Subject: тема, FromAddr: addr, FromName: name,
			Date: когда, Status: store.StatusPending, Confidence: уверен, Reason: почему,
			Account: "проба"}
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	июль := time.Date(2026, 7, 7, 12, 0, 0, 0, time.Local)
	август := time.Date(2026, 8, 26, 20, 30, 0, 0, time.Local)
	for i := 0; i < 5; i++ {
		добавить("<ozon"+string(rune('а'+i))+"@т>", "Ваш чек + (1) подарок",
			"noreply@sender.ozon.ru", "Ozon", июль.AddDate(0, 0, i), 0.85,
			"Подозрительная тема с обещанием подарка")
	}
	добавить("<ozon-поздний@т>", "Пора продлить подписку", "noreply@sender.ozon.ru", "Ozon",
		август, 0.85, "Подозрительная тема с обещанием подарка")
	добавить("<ecco@т>", "Розыгрыш промокода", "info@shop.example", "ECCO", август, 0.98,
		"Рекламный розыгрыш от неизвестного отправителя")

	doc, err := s.buildScreen("inbox", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	section := sectionByID(doc, "spam-groups")
	if section == nil {
		t.Fatalf("раздела спама нет:\n%s", clipTest(dumpScreen(doc)))
	}
	// две строки на семь писем: Ozon и ECCO
	if len(section.Children) != 2 {
		var что []string
		for _, c := range section.Children {
			что = append(что, c.Title+"/"+c.Subtitle)
		}
		t.Fatalf("строк %d, а отправителя два: %v", len(section.Children), что)
	}
	// Ozon первым: у него писем больше, там и выигрыш от одного нажатия
	ozon := section.Children[0]
	if ozon.Title != "Ozon" {
		t.Errorf("первым идёт %q, а не тот, у кого писем больше", ozon.Title)
	}
	if !strings.Contains(ozon.Subtitle, "6 писем") {
		t.Errorf("не сказано, сколько писем: %q", ozon.Subtitle)
	}
	// темы: частая первой, остальные числом. С 10 сентября 2026 они
	// стоят в пояснении строки, а не в подписи: подпись — это «сколько
	// писем», и мешать в неё темы было тесно
	if !strings.Contains(ozon.Caption, "«Ваш чек + (1) подарок»") {
		t.Errorf("частая тема не названа: %q", ozon.Caption)
	}
	if !strings.Contains(ozon.Caption, "и ещё 1 тема") {
		t.Errorf("остальные темы не посчитаны: %q", ozon.Caption)
	}
	// В строке — короткая причина, полная и разброс дат уехали в
	// раскрытие: причина от модели бывает в три предложения, и десять
	// таких строк превращали список в сплошной текст
	if !strings.Contains(ozon.Caption, "подарка") {
		t.Errorf("короткой причины в строке нет: %q", ozon.Caption)
	}
	if n := len([]rune(ozon.Caption)); n > 140 {
		t.Errorf("пояснение строки снова длинное: %d знаков — %q", n, ozon.Caption)
	}
	свойство := func(name string) string {
		for _, p := range ozon.Props {
			if p.Label == name {
				return p.Value
			}
		}
		return ""
	}
	if !strings.Contains(свойство("Почему"), "обещанием подарка") {
		t.Errorf("полное «Почему» потеряно, а не спрятано: %q", свойство("Почему"))
	}
	if свойство("Когда") != "с 7 июля по 26 августа" {
		t.Errorf("разброс дат потерян: %q", свойство("Когда"))
	}
	// решение на всех письмах отправителя
	var кнопки []string
	for _, a := range ozon.Actions {
		кнопки = append(кнопки, a.Label)
	}
	if !containsSubstring(кнопки, "Не спам — все 6") {
		t.Errorf("нет решения на всю группу: %v", кнопки)
	}
	if !containsSubstring(кнопки, "Показать письма") {
		t.Errorf("нельзя раскрыть группу: %v", кнопки)
	}
	// разрушительное спрашивает подтверждение
	for _, a := range ozon.Actions {
		if a.Action == "confirm_spam_sender" && a.Confirm == "" {
			t.Error("групповое «в спам» не спрашивает подтверждения")
		}
	}
}

// Отправителю с одним письмом группа не нужна: там сразу письмо, а не
// строка «1 письмо · «тема»» с кнопкой «Не спам — все 1».
func TestSingleMessageNeedsNoGroup(t *testing.T) {
	s, st := testServer(t)
	m := &store.Message{MessageID: "<один@т>", Subject: "Единственное письмо",
		FromAddr: "one@пример.рф", FromName: "Один", Date: time.Now(),
		Status: store.StatusPending, Confidence: 0.9, Reason: "похоже на рекламу", Account: "проба"}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("inbox", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpScreen(doc)
	if strings.Contains(dump, "все 1") {
		t.Error("у одного письма предложено решение «все 1»")
	}
	if !strings.Contains(dump, "Единственное письмо") {
		t.Errorf("самого письма нет:\n%s", clipTest(dump))
	}
}

// Кнопка «ещё» стоит внутри списка и говорит, сколько ДОБАВИТСЯ. Прежняя
// «Показать ещё (50 из 89)» стояла снаружи свёрнутой секции и обещала
// пятьдесят там, где показано ноль.
func TestMoreButtonSaysHowManyWillBeAdded(t *testing.T) {
	s, st := testServer(t)
	for i := 0; i < 15; i++ {
		а := string(rune('а'+i)) + "@пример.рф"
		m := &store.Message{MessageID: "<" + а + ">", Subject: "Тема", FromAddr: а,
			FromName: "Отправитель " + string(rune('а'+i)), Date: time.Now(),
			Status: store.StatusPending, Confidence: 0.9, Account: "проба"}
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := s.buildScreen("inbox", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	section := findSpamSection(t, doc)
	последняя := section.Children[len(section.Children)-1]
	if !strings.HasPrefix(последняя.Subtitle, "Ещё ") {
		t.Fatalf("последняя строка не кнопка «ещё»: %q", последняя.Subtitle)
	}
	if strings.Contains(последняя.Subtitle, "из") {
		t.Errorf("кнопка говорит «из», а должна — сколько добавится: %q", последняя.Subtitle)
	}
	if !strings.Contains(последняя.Subtitle, "3 отправителя") {
		t.Errorf("не сказано, сколько отправителей скрыто: %q", последняя.Subtitle)
	}
}

func findSpamSection(t *testing.T, doc *Document) *Component {
	t.Helper()
	р := sectionByID(doc, "spam-groups")
	if р == nil {
		t.Fatal("раздела спама нет")
	}
	return р
}

func containsSubstring(list []string, что string) bool {
	for _, s := range list {
		if strings.Contains(s, что) {
			return true
		}
	}
	return false
}

// sectionByID — раздел документа по его ID.
func sectionByID(doc *Document, id string) *Component {
	for i := range doc.Components {
		if doc.Components[i].ID == id {
			return &doc.Components[i]
		}
	}
	return nil
}

// «Не спам — все 41» касается всех писем отправителя, а не только тех,
// что попали на страницу списка. И отправитель встаёт в белый список —
// ровно как это делает возврат одного письма.
func TestDecisionOnAllSenderMessages(t *testing.T) {
	s, st := testServer(t)
	for i := 0; i < 6; i++ {
		m := &store.Message{MessageID: "<оз" + string(rune('а'+i)) + "@т>",
			Subject: "Ваш чек", FromAddr: "noreply@sender.ozon.ru", FromName: "Ozon",
			Date: time.Now(), Status: store.StatusPending, Confidence: 0.85, Account: "проба"}
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	// чужое письмо не должно пострадать
	чужое := &store.Message{MessageID: "<чужое@т>", Subject: "Другое", FromAddr: "other@пример.рф",
		Date: time.Now(), Status: store.StatusPending, Confidence: 0.9, Account: "проба"}
	if err := st.InsertMessage(чужое); err != nil {
		t.Fatal(err)
	}

	if _, err := s.execute(actionRequest{Action: "not_spam_sender", Target: "noreply@sender.ozon.ru"}); err != nil {
		t.Fatal(err)
	}
	ждут, err := st.ByStatus(store.StatusPending, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(ждут) != 1 || ждут[0].FromAddr != "other@пример.рф" {
		t.Errorf("после решения по отправителю осталось %d писем: %+v", len(ждут), ждут)
	}
	// отправитель в белом списке — иначе завтра те же письма придут снова
	rules, err := st.Rules()
	if err != nil {
		t.Fatal(err)
	}
	белый := false
	for _, r := range rules {
		if r.Kind == "sender" && r.Value == "noreply@sender.ozon.ru" && r.Action == "ham" {
			белый = true
		}
	}
	if !белый {
		t.Error("отправитель не попал в белый список")
	}
	// отправитель без ждущих писем — отказ, а не тихое «сделано»
	if _, err := s.execute(actionRequest{Action: "not_spam_sender", Target: "noreply@sender.ozon.ru"}); err == nil {
		t.Error("решение по отправителю без писем принято молча")
	}
}
