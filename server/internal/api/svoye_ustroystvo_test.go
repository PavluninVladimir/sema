package api

// Своё устройство (offline-snapshot, группа 3): отметка живёт у сессии, а не
// в браузере. Проверяем ровно то, что обещано design.md, решение 3: отметка
// ставится и снимается действием, отзыв сессии уносит её сам, а признак в
// документе получает только тот, у кого есть отмеченная сессия — локальный
// доступ и токен молчания не считают за разрешение.

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// sessionWithCookie — запрос с cookie сессии: тот же вход, что у браузера.
// Существующий helper «запрос» (security_regress_test.go) знает только
// Authorization, а отметка «моё» живёт у сессии passkey.
func sessionWithCookie(s *Server, method, path, token, remote string) *httptest.ResponseRecorder {
	if strings.HasPrefix(path, "/") {
		path = "http://localhost:8765" + path
	}
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = remote
	r.Header.Set(uiDictHeader, strconv.Itoa(UIDictVersion))
	if token != "" {
		r.Header.Set("Cookie", "sema_session="+token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// makeSession — passkey и открытая сессия с этим ключом; возвращает токен
// (кладётся в cookie) и хэш строки в базе (им действия адресуют сессию).
func makeSession(t *testing.T, s *Server, st *store.Store) (token, hash string) {
	t.Helper()
	id, err := st.AddPasskey("проба", "проба-ключ", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	token, err = s.auth.OpenSession(id, "проба", "203.0.113.10", "")
	if err != nil {
		t.Fatal(err)
	}
	return token, store.HashSecret(token)
}

func sessionByHash(t *testing.T, st *store.Store, hash string) *store.Session {
	t.Helper()
	sessions, err := st.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, se := range sessions {
		if se.Hash == hash {
			return se
		}
	}
	return nil
}

// Отметка ставится и снимается действием — ровно так, как жмёт кнопку
// человек на «Доступе».
func TestOwnMarkSetAndCleared(t *testing.T) {
	s, st := testServer(t)
	_, hash := makeSession(t, s, st)

	if se := sessionByHash(t, st, hash); se == nil || se.Own {
		t.Fatalf("свежая сессия уже отмечена своей")
	}
	if _, err := s.accessActions(actionRequest{Action: "mark_own_device", Target: hash}); err != nil {
		t.Fatalf("отметить своим: %v", err)
	}
	if se := sessionByHash(t, st, hash); se == nil || !se.Own {
		t.Fatalf("отметка не встала")
	}
	if _, err := s.accessActions(actionRequest{Action: "unmark_own_device", Target: hash}); err != nil {
		t.Fatalf("снять отметку: %v", err)
	}
	if se := sessionByHash(t, st, hash); se == nil || se.Own {
		t.Fatalf("отметка не снялась")
	}
}

// Отзыв сессии удаляет строку целиком — отметка не может пережить сессию,
// которой она принадлежала (design.md, решение 3).
func TestSessionRevokeTakesOwnMark(t *testing.T) {
	s, st := testServer(t)
	_, hash := makeSession(t, s, st)
	if _, err := s.accessActions(actionRequest{Action: "mark_own_device", Target: hash}); err != nil {
		t.Fatal(err)
	}
	if se := sessionByHash(t, st, hash); se == nil || !se.Own {
		t.Fatalf("не отметилась")
	}
	if _, err := s.accessActions(actionRequest{Action: "logout_session", Target: hash}); err != nil {
		t.Fatal(err)
	}
	if se := sessionByHash(t, st, hash); se != nil {
		t.Fatalf("сессия пережила отзыв: Own=%v", se.Own)
	}
}

// Документ несёт признак «это устройство отмечено своим» — молчание до
// отметки, разрешение после неё. Тот же самый экран, та же самая сессия.
func TestDocumentCarriesOwnDeviceFlag(t *testing.T) {
	s, st := testServer(t)
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	token, hash := makeSession(t, s, st)

	var doc Document
	w := sessionWithCookie(s, "GET", "/api/ui?screen=inbox", token, "203.0.113.10:1234")
	if w.Code != 200 {
		t.Fatalf("неотмеченное устройство: код %d, тело %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OwnDevice {
		t.Fatalf("неотмеченное устройство получило разрешение хранить снимок")
	}

	if _, err := s.accessActions(actionRequest{Action: "mark_own_device", Target: hash}); err != nil {
		t.Fatal(err)
	}
	w = sessionWithCookie(s, "GET", "/api/ui?screen=inbox", token, "203.0.113.10:1234")
	if w.Code != 200 {
		t.Fatalf("отмеченное устройство: код %d, тело %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.OwnDevice {
		t.Fatalf("отмеченное устройство не получило разрешение хранить снимок")
	}
}

// Запрос без сессии — локальный доступ или токен — разрешения не получает
// никогда: у обоих своей сессии в базе нет, и молчание здесь читается как
// «нет», а не «пока не решили».
func TestWithoutSessionOwnFlagNotSet(t *testing.T) {
	s, st := testServer(t)

	// локальный доступ: с этой машины Сёма работает без входа
	w := sessionWithCookie(s, "GET", "/api/ui?screen=inbox", "", "127.0.0.1:1234")
	if w.Code != 200 {
		t.Fatalf("локальный доступ: код %d", w.Code)
	}
	var doc Document
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OwnDevice {
		t.Fatalf("локальный доступ получил признак своего устройства")
	}

	// токен программы: своей сессии у него тоже нет
	value, err := st.AddToken("проба", []string{"ui"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(auth.SettingLocalOpen, "false"); err != nil {
		t.Fatal(err)
	}
	w = doRequest(s, "GET", "/api/ui?screen=inbox", "", "203.0.113.10:1234", value)
	if w.Code != 200 {
		t.Fatalf("токен: код %d, тело %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OwnDevice {
		t.Fatalf("токен получил признак своего устройства")
	}
}
