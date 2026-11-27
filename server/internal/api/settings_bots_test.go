package api

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

type messengerBotTestTransport func(*http.Request) (*http.Response, error)

func (fn messengerBotTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestMessengerBotSettingsWeb(t *testing.T) {
	s, st := testServer(t)
	const telegramToken = "100:TELEGRAM_TEST_TOKEN"
	const maxToken = "MAX_TEST_TOKEN"
	const maxReplacement = "MAX_REPLACEMENT_TEST_TOKEN"
	const otherBot = "MAX_OTHER_TEST_TOKEN"
	denied := false
	s.botTransport = messengerBotTestTransport(func(r *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, ""
		switch token := r.Header.Get("Authorization"); token {
		case "":
			body = `{"ok":true,"result":{"id":100,"is_bot":true,"first_name":"Северный помощник"}}`
		case maxToken, maxReplacement, otherBot:
			id := 100
			if token == otherBot {
				id++
			}
			body = fmt.Sprintf(`{"user_id":%d,"is_bot":true,"first_name":"Восточный помощник"}`, id)
			if denied {
				status, body = http.StatusUnauthorized, token
			}
		default:
			t.Fatal("неожиданный токен в запросе проверки")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	handler := s.Handler()
	visit := func(screen string, phone bool) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://localhost:8765/ui?screen="+url.QueryEscape(screen), nil)
		req.RemoteAddr = "127.0.0.1:5000"
		if phone {
			req.Header.Set("X-Sema-Phone", "1")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("экран %s: HTTP %d %s", screen, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	act := func(screen, action, target, token string) string {
		t.Helper()
		form := url.Values{"screen": {screen}, "_кнопка": {action + "|" + target}, "token": {token}}
		req := httptest.NewRequest(http.MethodPost, "http://localhost:8765/do", strings.NewReader(form.Encode()))
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code >= http.StatusInternalServerError {
			t.Fatalf("действие %s: HTTP %d %s", action, w.Code, w.Body.String())
		}
		for _, secret := range []string{telegramToken, maxToken, maxReplacement, otherBot} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("действие %s вернуло значение токена", action)
			}
		}
		return w.Body.String()
	}
	requireText := func(html string, phrases ...string) {
		t.Helper()
		for _, phrase := range phrases {
			if !strings.Contains(html, phrase) {
				t.Errorf("в разметке отсутствует %q", phrase)
			}
		}
	}
	for _, phone := range []bool{false, true} {
		requireText(visit(settingsRoot, phone), botsTitle)
		requireText(visit(tgScreen+"~conn", phone), "Telegram", botsTitle)
		requireText(visit(maxScreen, phone), "MAX", botsTitle)
		requireText(visit(botsScreen, phone), "Telegram", "MAX", "Добавить бота")
		requireText(visit(botsScreen+"@new~max", phone), "Добавить бота MAX", `type="password"`, "Подключить")
	}
	telegramNew, maxNew := botsScreen+"@new~telegram", botsScreen+"@new~max"
	requireText(act(telegramNew, "add_messenger_bot", "telegram", telegramToken), "Северный помощник", "ID 100", "Токен подтверждён")
	requireText(act(maxNew, "add_messenger_bot", "max", maxToken), "Восточный помощник", "ID 100", "Токен подтверждён")
	requireText(act(maxNew, "add_messenger_bot", "max", maxReplacement), "Восточный помощник")
	bots, err := st.MessengerBots()
	if err != nil || len(bots) != 2 {
		t.Fatalf("ожидалось два подключения: %d, %v", len(bots), err)
	}
	maxBot := bots[0] // сортировка по имени, «Восточный» перед «Северным»
	target := fmt.Sprint(maxBot.ID)
	card := botScreenAddr(maxBot.ID)
	requireText(act(card, "replace_messenger_bot_token", target, otherBot), "принадлежит другому боту")
	if got, err := st.MessengerBotToken(maxBot); err != nil || got != maxToken {
		t.Fatal("повторное добавление или ошибочная замена затронули токен")
	}
	denied = true
	requireText(act(card, "check_messenger_bot", target, ""), "Проверка не прошла", "Мессенджер отклонил токен")
	denied = false
	requireText(act(card, "replace_messenger_bot_token", target, maxReplacement), "Токен подтверждён")
	requireText(act(card, "delete_messenger_bot", target, ""), "Ботов пока нет")
	requireText(visit(botsScreen, true), "Северный помощник")
}

func TestMessengerBotSettingsGuestAccess(t *testing.T) {
	s, st := testServer(t)
	const name = "МаякАвтотеста"
	if _, err := st.AddMessengerBot(store.MessengerBot{ExternalID: 123, Platform: "max", Name: name, Status: "ok"}, "MAX_TEST_TOKEN"); err != nil {
		t.Fatal(err)
	}
	s.botTransport = messengerBotTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("гость вызвал Bot API")
		return nil, nil
	})
	guest, err := st.AddToken("читатель", []string{"ui", "actions"})
	if err != nil {
		t.Fatal(err)
	}
	if w := doRequest(s, http.MethodGet, "/api/ui?screen=settings:bots~max", "", снаружиАдрес, guest); w.Code != http.StatusForbidden {
		t.Fatalf("ограниченный токен открыл настройки ботов: %d", w.Code)
	}
	if w := doRequest(s, http.MethodPost, "/api/action", `{"screen":"settings","action":"add_messenger_bot","target":"max","params":{"token":"TOKEN"}}`, снаружиАдрес, guest); w.Code != http.StatusForbidden {
		t.Fatalf("ограниченный токен может выполнить действие: %d", w.Code)
	}
	w := doRequest(s, http.MethodGet, "/api/ui?screen=settings&search="+url.QueryEscape(botsTitle), "", снаружиАдрес, guest)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), botsScreen) {
		t.Fatalf("закрытый раздел попал в навигацию или поиск: %d", w.Code)
	}
	path := "/api/ui?screen=settings&search=" + url.QueryEscape(name)
	if w := doRequest(s, http.MethodGet, path, "", снаружиАдрес, guest); w.Code != http.StatusOK ||
		strings.Contains(w.Body.String(), botsScreen+"@") || strings.Contains(w.Body.String(), "ID 123") {
		t.Fatalf("гость нашёл закрытую карточку бота: %d", w.Code)
	}
	if w := doRequest(s, http.MethodGet, path, "", "127.0.0.1:5000", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), botsScreen+"@") {
		t.Fatalf("владелец не нашёл карточку бота: %d", w.Code)
	}
}
