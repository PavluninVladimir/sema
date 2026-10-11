package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Отказ канала по порогу — это замысел, а не беда, и впереди строки ему
// не место.
//
// 11 сентября 2026 на «Уведомлениях» стояло восемнадцать вопросов
// «Показать картинку модели?», и каждый начинался с «не доставлено ·
// Mac: порог канала выше уровня записи · Chrome на маке: порог канала
// выше уровня записи». Суть — какой файл и почему — обрезалась за краем
// экрана, и восемнадцать разных вопросов читались как один спам.
func TestSilenceByThresholdNotTrouble(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveNotifyChannel(&store.NotifyChannel{
		Kind: "mac", Title: "Mac", Enabled: true, MinLevel: "warn"}); err != nil {
		t.Fatal(err)
	}
	quiet := &store.Notification{Key: "vision:проба", Source: "core", Level: "info",
		Title: "Показать картинку модели?", Summary: "Файл «qr.png»: на картинке не нашлось текста"}
	id, err := e.Notify(quiet)
	if err != nil {
		t.Fatal(err)
	}
	record, has := st.Notification(id)
	if !has || record == nil {
		t.Fatal("записи нет")
	}
	silence := e.DeliveryChecker([]*store.Notification{record})
	if got := silence(record); got != "" {
		t.Errorf("порог канала выдан за недоставку: %q", got)
	}
}

// А выключенный канал — настоящая беда: запись никуда не пошла не
// потому, что не предназначалась, а потому что доставка сломана.
func TestDisabledChannelStillTrouble(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveNotifyChannel(&store.NotifyChannel{
		Kind: "mac", Title: "Mac", Enabled: false, MinLevel: "info"}); err != nil {
		t.Fatal(err)
	}
	id, err := e.Notify(&store.Notification{Key: "проба:беда", Source: "core", Level: "warn",
		Title: "Проверить продление домена"})
	if err != nil {
		t.Fatal(err)
	}
	record, has := st.Notification(id)
	if !has || record == nil {
		t.Fatal("записи нет")
	}
	got := e.DeliveryChecker([]*store.Notification{record})(record)
	if !strings.Contains(got, "не доставлено") || !strings.Contains(got, "выключен") {
		t.Errorf("сломанная доставка замолчала: %q", got)
	}
}

// Тихие часы тоже не порог: запись предназначалась, но её придержали.
func TestQuietHoursStayVisible(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	hour := time.Now().Hour()
	s := time.Date(0, 1, 1, hour, 0, 0, 0, time.UTC).Format("15:04")
	by := time.Date(0, 1, 1, (hour+2)%24, 0, 0, 0, time.UTC).Format("15:04")
	if err := st.SaveNotifyChannel(&store.NotifyChannel{
		Kind: "mac", Title: "Mac", Enabled: true, MinLevel: "info",
		QuietFrom: s, QuietTo: by}); err != nil {
		t.Fatal(err)
	}
	id, err := e.Notify(&store.Notification{Key: "проба:тихо", Source: "core", Level: "warn",
		Title: "Ночное дело"})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := st.Notification(id)
	if record == nil {
		t.Fatal("записи нет")
	}
	if got := e.DeliveryChecker([]*store.Notification{record})(record); got == "" {
		t.Error("тихие часы приравнены к порогу — человек не узнает, что запись придержана")
	}
}

// Вопрос «Показать картинку модели?» не задаётся про письма, которые
// Сёма не разбирает. Спам и корзину почтового сервера он читает на
// месте: не судит и моделям не показывает — значит и решение человека
// там ни на что не влияет.
//
// 11 сентября 2026 разбор чужих папок принёс сотни писем с
// картинками-пикселями, и на каждую Сёма спрашивал отдельно.
func TestAboutForeignSpamAboutImageNotAsk(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	cases := map[string]string{
		"спам провайдера": store.StatusServerSpam,
		"корзина":         store.StatusServerTrash,
	}
	i := 0
	for name, status := range cases {
		i++
		m := &store.Message{Account: "я@почта", MessageID: fmt.Sprintf("<%d@проба>", i),
			FromAddr: "кто@то", Subject: "рассылка", Status: status, Date: time.Now()}
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
		hash := fmt.Sprintf("%064d", i)
		if err := st.InsertAttachment(&store.Attachment{MessageID: m.ID,
			Filename: "pixel.gif", MIME: "image/gif", Path: "/data/files/" + hash, SHA256: hash}); err != nil {
			t.Fatal(err)
		}
		e.askShowImage("/tmp/"+hash, "на картинке не нашлось текста")
		if _, has := st.OpenNotificationByKey("vision:" + hash); has {
			t.Errorf("%s: задан вопрос про картинку из письма, которое Сёма не разбирает", name)
		}
	}
}

// А про обычное письмо спрашиваем — иначе картинка уехала бы облачной
// модели без спроса.
func TestAboutPlainMessageAsk(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	m := &store.Message{Account: "я@почта", MessageID: "<обычное@проба>", FromAddr: "кто@то",
		Subject: "счёт", Status: store.StatusInboxOK, Date: time.Now()}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%064d", 77)
	if err := st.InsertAttachment(&store.Attachment{MessageID: m.ID, Filename: "скан.png",
		MIME: "image/png", Path: "/data/files/" + hash, SHA256: hash}); err != nil {
		t.Fatal(err)
	}
	e.askShowImage("/tmp/"+hash, "на картинке не нашлось текста")
	z, has := st.OpenNotificationByKey("vision:" + hash)
	if !has {
		t.Fatal("про обычное письмо вопрос не задан — картинка уедет молча")
	}
	if !strings.Contains(z.Summary, "скан.png") {
		t.Errorf("в вопросе не настоящее имя файла: %q", z.Summary)
	}
}

// Шестой неотвеченный вопрос подряд не задаётся: человек уже видит, что
// их много, и ответит разом настройкой.
func TestQuestionsAboutImageHaveLimit(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SetSetting("vision.max_ask", "3"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 6; i++ {
		e.askShowImage(fmt.Sprintf("/tmp/%064d", i), "на картинке не нашлось текста")
	}
	if got := e.howManyImageQuestionsWait(); got != 3 {
		t.Errorf("висит %d вопросов при пределе 3", got)
	}
}
