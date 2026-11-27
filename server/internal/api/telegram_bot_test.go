package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/botapi"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestTelegramBotWebhookAndFeatures(t *testing.T) {
	s, st := testServer(t)
	const host, token = "https://sema.example.org", "100:TEST_TELEGRAM_RUNTIME_TOKEN"
	if err := st.SetSetting(auth.SettingOrigin, host); err != nil {
		t.Fatal(err)
	}
	id, err := st.AddMessengerBot(store.MessengerBot{
		Platform: botapi.Telegram, ExternalID: 100, Name: "Помощник", Username: "helper_bot", Status: "ok",
	}, token)
	if err != nil {
		t.Fatal(err)
	}
	path := telegramWebhookPath + strconv.FormatInt(id, 10)
	settingsURL := fmt.Sprintf("/api/bots/%d/telegram", id)
	const local = "127.0.0.1:5000"
	var webhookURL, webhookSecret string
	calls := map[string]int{}
	var sends []map[string]any
	s.botTransport = messengerBotTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" {
			t.Fatal("запрос ушёл мимо Bot API")
		}
		method, ok := strings.CutPrefix(r.URL.Path, "/bot"+token+"/")
		if !ok {
			t.Fatal("неверный запрос Telegram")
		}
		calls[method]++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		var result any = true
		switch method {
		case "getWebhookInfo":
			result = map[string]any{"url": webhookURL, "pending_update_count": 1}
		case "setWebhook":
			webhookURL, _ = payload["url"].(string)
			webhookSecret, _ = payload["secret_token"].(string)
			if webhookURL != host+path || len(webhookSecret) != 43 {
				t.Fatal("Telegram не получил URL и отдельный секрет")
			}
		case "deleteWebhook":
			webhookURL = ""
		case "sendMessage":
			sends = append(sends, payload)
			result = map[string]any{"message_id": 31}
		case "getChatMember":
			status := "member"
			if payload["user_id"] == float64(77) {
				status = "administrator"
			}
			result = map[string]any{"status": status}
		case "deleteMessage":
			if payload["chat_id"] != float64(-1009) {
				t.Error("удаление вне разрешённой группы")
			}
		default:
			t.Fatalf("неожиданная операция %s", method)
		}
		raw, err := json.Marshal(map[string]any{"ok": true, "result": result})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	expect := func(w *httptest.ResponseRecorder, status int) string {
		t.Helper()
		body := w.Body.String()
		if strings.Contains(body, token) || (webhookSecret != "" && strings.Contains(body, webhookSecret)) {
			t.Fatal("ответ содержит секрет")
		}
		if w.Code != status {
			t.Fatalf("HTTP %d вместо %d: %s", w.Code, status, body)
		}
		return body
	}
	guest, err := st.AddToken("читатель", []string{"ui", "actions"})
	if err != nil {
		t.Fatal(err)
	}
	expect(doRequest(s, http.MethodGet, settingsURL, "", снаружиАдрес, ""), http.StatusUnauthorized)
	expect(doRequest(s, http.MethodPatch, settingsURL, `{"webhook":true}`, снаружиАдрес, guest), http.StatusForbidden)
	owner, err := st.AddToken("владелец", []string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	read := expect(doRequest(s, http.MethodGet, settingsURL, "", снаружиАдрес, owner), http.StatusOK)
	if !strings.Contains(read, host+path) || !strings.Contains(read, `"webhook":false`) {
		t.Fatalf("нет доступного URL для выключенного webhook: %s", read)
	}
	card := expect(doRequest(s, http.MethodGet, "/ui?screen="+url.QueryEscape(botScreenAddr(id)), "", local, ""), http.StatusOK)
	for _, phrase := range []string{"Включить webhook", "Скопировать URL", "Функции бота", "Защищать от флуда", host + path} {
		if !strings.Contains(card, phrase) {
			t.Errorf("на карточке нет %q", phrase)
		}
	}

	webhookURL = "https://elsewhere.example.org/callback"
	expect(doRequest(s, http.MethodPatch, settingsURL, `{"webhook":true}`, снаружиАдрес, owner), http.StatusBadRequest)
	if calls["setWebhook"] != 0 {
		t.Fatal("чужой webhook перезаписан")
	}
	webhookURL = ""
	expect(doRequest(s, http.MethodPatch, settingsURL, `{"features":{"groups":[-1009],"block_links":true,"block_words":true,"words":["предлагаю займ"],"commands":true,"start_text":"Начали","welcome":true,"welcome_text":"Добро пожаловать в группу","private_reply":true}}`, снаружиАдрес, owner), http.StatusOK)
	form := url.Values{"screen": {botScreenAddr(id)}, "_кнопка": {"set_telegram_bot_webhook|" + strconv.FormatInt(id, 10)}, "webhook": {"true"}, "toggles": {"webhook"}}
	req := httptest.NewRequest(http.MethodPost, "http://localhost:8765/do", strings.NewReader(form.Encode()))
	req.RemoteAddr = local
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	expect(w, http.StatusOK)
	if calls["setWebhook"] != 1 {
		t.Fatal("переключатель карточки не зарегистрировал webhook")
	}
	expect(doRequest(s, http.MethodPost, settingsURL+"/send", `{"chat_id":123,"text":"Тест"}`, снаружиАдрес, owner), http.StatusOK)

	event := func(secret, payload string, status int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, host+path, strings.NewReader(payload))
		req.RemoteAddr = снаружиАдрес
		req.Header.Set(botapi.TelegramWebhookSecretHeader, secret)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		expect(w, status)
	}
	start := `{"update_id":1,"message":{"message_id":1,"chat":{"type":"private","id":123},"from":{"id":123},"text":"/start@helper_bot"}}`
	event(strings.Repeat("x", 43), start, http.StatusNotFound)
	event(webhookSecret, start, http.StatusNoContent)
	event(webhookSecret, start, http.StatusNoContent)
	event(webhookSecret, `{"update_id":2,"message":{"message_id":2,"chat":{"type":"supergroup","id":-1009},"from":{"id":12},"text":"смотрите https://example.net"}}`, http.StatusNoContent)
	event(webhookSecret, `{"update_id":3,"edited_message":{"message_id":3,"chat":{"type":"supergroup","id":-1009},"from":{"id":77},"caption_entities":[{"type":"text_link"}]}}`, http.StatusNoContent)
	event(webhookSecret, `{"update_id":4,"message":{"message_id":4,"chat":{"type":"supergroup","id":-1010},"from":{"id":12},"text":"https://example.net"}}`, http.StatusNoContent)
	event(webhookSecret, `{"update_id":5,"message":{"message_id":5,"message_thread_id":54,"chat":{"type":"supergroup","id":-1009},"from":{"id":12},"new_chat_members":[{"id":49}]}}`, http.StatusNoContent)
	event(webhookSecret, `{"update_id":6,"message":{"message_id":6,"chat":{"type":"private","id":123},"from":{"id":123},"text":"Добрый день"}}`, http.StatusNoContent)
	if calls["deleteMessage"] != 1 || calls["getChatMember"] != 2 {
		t.Errorf("фильтр не различил участника, администратора и чужую группу: %v", calls)
	}
	if len(sends) != 4 || sends[1]["text"] != "Начали" ||
		sends[2]["text"] != "Добро пожаловать в группу" || sends[2]["message_thread_id"] != float64(54) ||
		sends[3]["text"] != "Спасибо за сообщение." {
		t.Errorf("неверные ответы бота: %v", sends)
	}

	expect(doRequest(s, http.MethodPatch, settingsURL, `{"features":{"groups":[-1009],"block_flood":true}}`, снаружиАдрес, owner), http.StatusOK)
	for i := 0; i < 6; i++ {
		event(webhookSecret, fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"chat":{"type":"supergroup","id":-1009},"from":{"id":21},"text":"проба"}}`, 100+i, 100+i), http.StatusNoContent)
	}
	if calls["deleteMessage"] != 2 {
		t.Error("сработал неверный порог флуда")
	}
	oldSecret := webhookSecret
	expect(doRequest(s, http.MethodPatch, settingsURL, `{"webhook":false}`, снаружиАдрес, owner), http.StatusOK)
	if calls["deleteWebhook"] != 1 {
		t.Error("Telegram не получил отключение webhook")
	}
	event(oldSecret, start, http.StatusNotFound)
	state, err := st.TelegramBotStateFor(id)
	if err != nil || state.WebhookURL != "" || !state.Features.BlockFlood || !state.Features.PrivateReply ||
		state.LastGroup != -1009 || state.LastDelivery == 0 || time.Since(time.UnixMilli(state.LastDelivery)) > time.Minute {
		t.Fatalf("неверное итоговое состояние: %+v, %v", state, err)
	}
}

func TestBrowserTelegramBotWebhookForm(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: SEMA_BROWSER=1")
	}
	s, st := testServer(t)
	const host = "https://sema.example.org"
	if err := st.SetSetting(auth.SettingOrigin, host); err != nil {
		t.Fatal(err)
	}
	id, err := st.AddMessengerBot(store.MessengerBot{
		Platform: botapi.Telegram, ExternalID: 101, Name: "Telegram-бот", Status: "ok",
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}, "101:TEST_TELEGRAM_RUNTIME_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	var enabled atomic.Bool
	s.botTransport = messengerBotTestTransport(func(r *http.Request) (*http.Response, error) {
		var result any = true
		switch {
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			address := ""
			if enabled.Load() {
				address = host + telegramWebhookPath + strconv.FormatInt(id, 10)
			}
			result = map[string]string{"url": address}
		case strings.HasSuffix(r.URL.Path, "/setWebhook"):
			enabled.Store(true)
		case strings.HasSuffix(r.URL.Path, "/deleteWebhook"):
			enabled.Store(false)
		}
		raw, err := json.Marshal(map[string]any{"ok": true, "result": result})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, time.Minute)
	karkasOpen(t, ctx, botScreenAddr(id))
	for _, checked := range []bool{true, false} {
		var dirty, rendered bool
		err := chromedp.Run(ctx,
			chromedp.Click(`input[name="webhook"] + label.field-toggle`, chromedp.ByQuery),
			chromedp.Poll(`!document.getElementById('save-bar')?.hidden`, &dirty, chromedp.WithPollingTimeout(10*time.Second)),
			chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery),
			chromedp.Poll(`document.querySelector('input[name="webhook"]')?.defaultChecked === `+strconv.FormatBool(checked)+
				` && document.getElementById('save-bar')?.hidden`, &rendered, chromedp.WithPollingTimeout(10*time.Second)))
		if err != nil || !dirty || !rendered || enabled.Load() != checked {
			t.Fatalf("переключатель webhook, ожидалось %v: %v", checked, err)
		}
		if checked {
			if err := chromedp.Run(ctx, chromedp.Poll(`!document.getElementById('splash')`, nil,
				chromedp.WithPollingTimeout(10*time.Second))); err != nil {
				t.Fatal(err)
			}
			karkasShot(t, ctx, "telegram-webhook-phone")
		}
	}
	var fits bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.scrollWidth <= document.documentElement.clientWidth`, &fits)); err != nil || !fits {
		t.Errorf("карточка бота шире окна телефона: %v", err)
	}
}
