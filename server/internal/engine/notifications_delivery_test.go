package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Запись со сроком — не «к сведению». На живой Сёме из 148 записей уровня
// «инфо» 144 не ушли никуда, и среди них «Пополнить баланс для продления
// сервера» и «оплатить сервер»: каналы стояли с порогом «внимание», а
// напоминание об оплате рождалось с «инфо».
func TestRecordWithDueNotInfo(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	due := time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	id, err := e.Notify(&store.Notification{Key: "проба:срок", Source: "mail", Level: "info",
		Title: "Пополнить баланс для продления сервера", Due: due})
	if err != nil {
		t.Fatal(err)
	}
	n, ok := e.Store().Notification(id)
	if !ok {
		t.Fatal("записи нет")
	}
	if store.LevelRank(n.Level) < store.LevelRank("warn") {
		t.Errorf("у записи со сроком уровень %q — она не пройдёт порог каналов", n.Level)
	}
	// а без срока «инфо» остаётся «инфо»: поднимать всё подряд значит
	// сделать порог бессмысленным
	id2, err := e.Notify(&store.Notification{Key: "проба:без", Source: "mail", Level: "info",
		Title: "Пришло письмо"})
	if err != nil {
		t.Fatal(err)
	}
	if n2, _ := e.Store().Notification(id2); n2.Level != "info" {
		t.Errorf("запись без срока подняли до %q", n2.Level)
	}
}

// Недоставленное видно. Молчащий ассистент выглядит как спокойная жизнь:
// человек не получил запись и не знает, что не получил.
func TestUndeliveredVisible(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveNotifyChannel(&store.NotifyChannel{
		Kind: "mac", Title: "Мак", Enabled: true, MinLevel: "warn"}); err != nil {
		t.Fatal(err)
	}
	id, err := e.Notify(&store.Notification{Key: "проба:1", Source: "core", Level: "alert",
		Title: "Проверить продление домена", Summary: "срок сегодня"})
	if err != nil {
		t.Fatal(err)
	}
	n, _ := e.Store().Notification(id)
	if !n.Undelivered() {
		t.Fatal("канал доставки поддельный, доставить было некому — запись должна считаться недоставленной")
	}
	why := e.DeliveryChecker([]*store.Notification{n})
	res := why(n)
	if !strings.Contains(res, "не доставлено") {
		t.Errorf("молчание не объяснено: %q", res)
	}
	not, total, _ := e.DeliveryWeek()
	if total == 0 || not == 0 {
		t.Errorf("сводка за неделю: не доставлено %d из %d", not, total)
	}

	// доставленная молчать не должна
	st.MarkDelivered(id, "mac", "")
	n, _ = e.Store().Notification(id)
	if n.Undelivered() {
		t.Error("после успешной доставки запись всё ещё «не дошла»")
	}
	if s := e.DeliveryChecker([]*store.Notification{n})(n); s != "" {
		t.Errorf("доставленная объявлена молчащей: %q", s)
	}
}

// Отметка о доставке ставится только за успех. Раньше туда писалась и
// неудача, и вопрос «дошло ли хоть куда-нибудь» переставал иметь ответ.
func TestDenialNotCountsDelivery(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	id, err := e.Notify(&store.Notification{Key: "проба:1", Source: "core", Level: "alert", Title: "Проба"})
	if err != nil {
		t.Fatal(err)
	}
	st.MarkDelivered(id, "web", "подписанных устройств нет")
	n, _ := e.Store().Notification(id)
	if !n.Undelivered() {
		t.Error("отказ доставки записан как доставка")
	}
	// причина при этом сохранена: человеку её и показываем
	if fails := st.NotifyFails([]int64{id}); !strings.Contains(fails[id], "подписанных устройств нет") {
		t.Errorf("причина отказа потеряна: %q", fails[id])
	}
}
