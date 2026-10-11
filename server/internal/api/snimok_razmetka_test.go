package api

// Признаки снимка доезжают до браузера (offline-snapshot, 4.0).
//
// Проверка идёт путём `/ui` — тем, которым ходит единственный живой клиент,
// а не `/api/ui`: ручку снятого мак-клиента проверять бессмысленно, на этом
// уже обжигались со сжатием, которое годами стояло на мёртвой дороге и
// ничего не сжимало. Здесь смотрим готовую разметку: `Document.Storable`,
// `SnapshotDiffers`, `OwnDevice` и `Action.Deferrable` сервер проставлял и
// раньше, но до этой правки ни один из них в разметку не попадал вовсе.

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/auth"
)

// screenMarkup — экран так, как его получает браузер: путь `/ui`, ответ
// HTML. Пустой токен — запрос «с этой машины», иначе сессия по cookie.
func screenMarkup(t *testing.T, s *Server, screen, token, remote string) string {
	t.Helper()
	r := httptest.NewRequest("GET", "http://localhost:8765/ui?screen="+screen, nil)
	r.Header.Set("HX-Request", "true")
	r.RemoteAddr, r.Host = remote, "127.0.0.1:8765"
	if token != "" {
		r.Header.Set("Cookie", "sema_session="+token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("экран %q: код %d, тело %s", screen, w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="screen"`) {
		t.Fatalf("экран %q: это не разметка экрана:\n%s", screen, body)
	}
	return body
}

// screenAttr — значение data-атрибута у секции `#screen`.
func screenAttr(t *testing.T, markup, name string) string {
	t.Helper()
	метка := " data-" + name + `="`
	i := strings.Index(markup, метка)
	if i < 0 {
		// с новой строки: в шаблоне атрибуты стоят в несколько строк
		метка = "\ndata-" + name + `="`
		if i = strings.Index(markup, метка); i < 0 {
			метка = "  data-" + name + `="`
			i = strings.Index(markup, метка)
		}
	}
	if i < 0 {
		return ""
	}
	хвост := markup[i+len(метка):]
	j := strings.Index(хвост, `"`)
	if j < 0 {
		return ""
	}
	return хвост[:j]
}

func TestSnapshotFlagsTravelInScreenMarkup(t *testing.T) {
	s, st := testServer(t)
	// вход нужен всем: иначе запрос считается локальным, а у локального
	// своей сессии нет и отметки «моё устройство» тоже
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	token, hash := makeSession(t, s, st)
	const издалека = "203.0.113.10:1234"

	// 1. Устройство не отмечено — признак «своё» молчит, и это «нет», а не
	// «пока не решили»
	до := screenMarkup(t, s, "inbox", token, издалека)
	if got := screenAttr(t, до, "own-device"); got != "0" {
		t.Errorf("неотмеченное устройство: data-own-device=%q, ждали «0»", got)
	}

	if _, err := s.accessActions(actionRequest{Action: "mark_own_device", Target: hash}); err != nil {
		t.Fatal(err)
	}

	// 2. Хранимый экран: все четыре признака на месте и читаются
	markup := screenMarkup(t, s, "inbox", token, издалека)
	if got := screenAttr(t, markup, "storable"); got != "1" {
		t.Errorf("«Почта» должна быть хранимой: data-storable=%q", got)
	}
	if got := screenAttr(t, markup, "own-device"); got != "1" {
		t.Errorf("отмеченное устройство: data-own-device=%q, ждали «1»", got)
	}
	if got := screenAttr(t, markup, "dictionary"); got != strconv.Itoa(UIDictVersion) {
		t.Errorf("версия словаря в ключе снимка: data-dictionary=%q, а словарь %d", got, UIDictVersion)
	}
	if got := screenAttr(t, markup, "snapshot-days"); got != "7" {
		t.Errorf("срок снимка по умолчанию — неделя, а в разметке %q", got)
	}
	// «Почта» несёт письма, значит снимок у неё всегда другой — и без
	// закрытых ящиков тоже: «секретное» снимочная сборка вырезает всегда.
	// Раньше здесь ждали «0», потому что признак зависел от закрытых ящиков,
	// а в доме, где их нет, клиент складывал показанную сборку и отсев
	// «секретного» до устройства не доходил вовсе.
	if got := screenAttr(t, markup, "snapshot-differs"); got != "1" {
		t.Errorf("«Почта» несёт письма — снимок обязан отличаться, а в разметке %q", got)
	}
	// А экран, письма нести не способный, второго запроса не просит.
	if got := screenAttr(t, screenMarkup(t, s, "health", token, издалека), "snapshot-differs"); got != "0" {
		t.Errorf("«Здоровье» писем не показывает — снимку отличаться нечем, а в разметке %q", got)
	}

	// 3. Срок не забит гвоздями: настройка правится и доезжает
	if err := st.SetSetting("ui.snapshot_days", "3"); err != nil {
		t.Fatal(err)
	}
	if got := screenAttr(t, screenMarkup(t, s, "inbox", token, издалека), "snapshot-days"); got != "3" {
		t.Errorf("ui.snapshot_days=3, а в разметке %q — значение забито в код", got)
	}

	// 4. Опасный экран: хранить нельзя, и в разметке это видно
	for _, screen := range []string{"settings:secrets", "sent", "settings:access"} {
		if got := screenAttr(t, screenMarkup(t, s, screen, token, издалека), "storable"); got != "0" {
			t.Errorf("экран %q не должен храниться, а data-storable=%q", screen, got)
		}
	}
}

// Откладываемость тоже доезжает, и ровно к тем двум местам, которым
// сервер её выдал: отправке сообщения и прикреплению файла.
func TestDeferrableTravelsInMarkup(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	token, _ := makeSession(t, s, st)
	const издалека = "203.0.113.10:1234"

	chatID, err := st.CreateChat("Проба снимка")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddChatMsg(chatID, "user", "привет"); err != nil {
		t.Fatal(err)
	}
	чат := screenMarkup(t, s, "chat:"+strconv.FormatInt(chatID, 10), token, издалека)

	// композер и скрепка — ровно два места с признаком
	if n := strings.Count(чат, `data-deferrable="1"`); n != 2 {
		t.Errorf("в чате должно быть два откладываемых места (отправка и скрепка), а их %d", n)
	}
	i := strings.Index(чат, `class="composer"`)
	if i < 0 {
		t.Fatalf("в чате нет композера")
	}
	конецФормы := i + strings.Index(чат[i:], ">")
	if !strings.Contains(чат[i:конецФормы], `data-deferrable="1"`) {
		t.Errorf("форма композера без признака откладываемости:\n%s", чат[i:конецФормы])
	}

	// На экране без композера откладывать нечего: признака нет ни у одной
	// кнопки — ни у подтверждения команды, ни у спама, ни у удаления
	for _, screen := range []string{"inbox", "notifications", "settings:servers", "tasks"} {
		if n := strings.Count(screenMarkup(t, s, screen, token, издалека), "data-deferrable"); n != 0 {
			t.Errorf("экран %q: %d откладываемых действий, а их там быть не должно", screen, n)
		}
	}
}
