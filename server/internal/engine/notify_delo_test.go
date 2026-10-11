package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Одно дело — одна запись: провайдер присылает напоминание каждый день, и
// каждое письмо не должно заводить новую строку (13 сентября 2026 их было
// три об одном продлении).
func TestMatterMessagesOneOnReminders(t *testing.T) {
	one := messageMatter("robot@hosting.example",
		"Пополнить баланс на 512,30 руб. для продления сервера RU2540 до 15.09.2026")
	other := messageMatter("robot@hosting.example",
		"Re: Пополнить баланс на 512,00 руб. для продления сервера RU2540 до 18.09.2026")
	if one == "" || one != other {
		t.Errorf("дела разошлись:\n%q\n%q", one, other)
	}
	// разные дела одного отправителя не сливаются
	if count := messageMatter("robot@hosting.example", "Акт сверки за август"); count == one {
		t.Error("акт сверки принят за продление")
	}
	// разные отправители — разные дела
	if foreign := messageMatter("noreply@itshop.pro", "Пополнить баланс для продления сервера"); foreign == one {
		t.Error("письма разных отправителей слиты в одно дело")
	}
	// без темы ключ по делу не строится: пусть зовущий берёт номер письма
	if messageMatter("robot@hosting.example", "  ") != "" {
		t.Error("пустая тема дала ключ дела")
	}
}

// Письмо об оплате закрывает дела того же отправителя: человек заплатил,
// напоминанию нечего ждать. Просьба «оплатите» ничего не закрывает.
func TestMessageAboutPaymentClosesMatter(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	matter := messageMatter("robot@hosting.example", "Пополнить баланс для продления сервера RU2540")
	if _, err := st.PutNotification(&store.Notification{
		Key: matter, Source: "mail", Level: "warn", Title: "Пополнить баланс", Ref: "mail:1"}); err != nil {
		t.Fatal(err)
	}
	foreign := messageMatter("other@example.com", "Пополнить баланс")
	if _, err := st.PutNotification(&store.Notification{
		Key: foreign, Source: "mail", Level: "warn", Title: "Чужое дело", Ref: "mail:2"}); err != nil {
		t.Fatal(err)
	}
	// просьба заплатить ничего не закрывает
	if n := e.closeSenderMatters("robot@hosting.example", "Оплатите счёт", "к оплате 512,30 руб."); n != 0 {
		t.Fatalf("просьба «оплатите» закрыла %d записей", n)
	}
	if n := e.closeSenderMatters("robot@hosting.example", "Платёж получен", "спасибо, баланс пополнен"); n != 1 {
		t.Fatalf("подтверждение оплаты закрыло %d записей, ждали 1", n)
	}
	if rec, ok := st.OpenNotificationByKey(matter); ok {
		t.Errorf("дело осталось открытым: %+v", rec)
	}
	if _, ok := st.OpenNotificationByKey(foreign); !ok {
		t.Error("закрыто дело другого отправителя")
	}
}

// Письмо про деньги заводит запись, даже если отправитель робот: раньше
// «не хватает средств для автоматического продления» попадало в группу
// «Уведомления» с видом «ничего не заводить» и не доходило вовсе.
func TestRuleAboutMoneyAboveGroup(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	e.ensureNotifySeed()
	rules, err := e.store.NotifyRules()
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"group":   "notifications",
		"subject": "Уведомление о нехватке средств для автоматического продления",
		"text":    "Уведомляем, что на вашем балансе недостаточно средств для продления товаров",
		"from":    "noreply@itshop.pro",
	}
	var fired *store.NotifyRule
	for _, r := range rules {
		if matchNotifyRule(r, payload) {
			fired = r
			break
		}
	}
	if fired == nil {
		t.Fatal("ни одно правило не сработало на письмо про нехватку средств")
	}
	if fired.Kind == "none" {
		t.Errorf("сработало правило «%s» с видом «ничего не заводить» — письмо про деньги не дойдёт", fired.Title)
	}
}

// Записи, заведённые до ключа-дела, сводятся разовым проходом: свежая
// остаётся, старые помечаются заменёнными; оплаченное закрывается.
func TestConsolidateMattersOnce(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	message := func(n int, subject string, when time.Time) int64 {
		m := &store.Message{Account: "проба", MessageID: fmt.Sprintf("<%d@тест>", n), UID: uint32(n),
			FromAddr: "robot@hosting.example", Subject: subject, Date: when, Status: store.StatusArchived}
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	long, recently := time.Now().Add(-72*time.Hour), time.Now().Add(-24*time.Hour)
	old := message(1, "Пополнить баланс на 512,30 руб. для продления сервера RU2540", long)
	fresh := message(2, "Пополнить баланс на 512,00 руб. для продления сервера RU2540", recently)
	for _, p := range []struct {
		id  int64
		key string
	}{{old, "mail:1"}, {fresh, "mail:2"}} {
		if _, err := st.PutNotification(&store.Notification{Key: p.key, Source: "mail", Level: "warn",
			Title: "Пополнить баланс", Ref: fmt.Sprintf("mail:%d", p.id)}); err != nil {
			t.Fatal(err)
		}
	}
	// проход идёт от свежих к старым: порядок задаёт сам список
	e.ConsolidateMattersOnce()
	open := 0
	list, _ := st.Notifications(true, 100)
	for _, n := range list {
		if strings.HasPrefix(n.Title, "Пополнить баланс") {
			open++
		}
	}
	if open != 1 {
		t.Errorf("после сведения открытых записей об одном деле %d, ждали 1", open)
	}
	// второй раз проход ничего не делает: он разовый
	if _, err := st.PutNotification(&store.Notification{Key: "mail:3", Source: "mail", Level: "warn",
		Title: "Пополнить баланс", Ref: fmt.Sprintf("mail:%d", fresh)}); err != nil {
		t.Fatal(err)
	}
	e.ConsolidateMattersOnce()
	list, _ = st.Notifications(true, 100)
	was := 0
	for _, n := range list {
		if strings.HasPrefix(n.Title, "Пополнить баланс") {
			was++
		}
	}
	if was != 2 {
		t.Errorf("разовый проход сработал второй раз: открытых %d", was)
	}
}

// Подтверждение оплаты приходит когда придёт: задача «Сроки» закрывает
// записи, дело которых уже оплачено. Слова берём как пишут провайдеры —
// «Подтверждение оплаты», «Уведомление о зачислении платежа».
func TestClosePaidByArchive(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	count := &store.Message{Account: "проба", MessageID: "<счёт@тест>", UID: 1,
		FromAddr: "info@hosting.example", Subject: "Ваш расчётный период подходит к концу",
		Date: time.Now().Add(-6 * time.Hour), Status: store.StatusArchived}
	if err := st.InsertMessage(count); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutNotification(&store.Notification{Key: "дело:info@hosting.example:расчётный период",
		Source: "mail", Level: "warn", Title: "Пополнить баланс для продления сервера",
		Ref: fmt.Sprintf("mail:%d", count.ID)}); err != nil {
		t.Fatal(err)
	}
	// пока подтверждения нет — запись открыта
	if n := e.ClosePaid(); n != 0 {
		t.Fatalf("закрыл %d записей без подтверждения оплаты", n)
	}
	payment := &store.Message{Account: "проба", MessageID: "<оплата@тест>", UID: 2,
		FromAddr: "info@hosting.example", Subject: "Подтверждение оплаты",
		Snippet: "спасибо, платёж на 512,30 руб. учтён", Date: time.Now(), Status: store.StatusArchived}
	if err := st.InsertMessage(payment); err != nil {
		t.Fatal(err)
	}
	if n := e.ClosePaid(); n != 1 {
		t.Fatalf("после подтверждения закрыто %d записей, ждали 1", n)
	}
	list, _ := st.Notifications(true, 50)
	for _, n := range list {
		if strings.Contains(n.Title, "Пополнить баланс") {
			t.Error("оплаченное дело осталось открытым")
		}
	}
	// приметы провайдеров ловятся все
	for _, subject := range []string{"Подтверждение оплаты", "Уведомление о зачислении платежа",
		"Pally: Произведена оплата.", "Payment confirmation"} {
		if !messageAboutPayment(subject, "") {
			t.Errorf("не узнал подтверждение оплаты в теме %q", subject)
		}
	}
	for _, subject := range []string{"Оплатите счёт", "Счёт на оплату", "Срок оплаты подходит"} {
		if messageAboutPayment(subject, "") {
			t.Errorf("просьба заплатить принята за подтверждение: %q", subject)
		}
	}
}
