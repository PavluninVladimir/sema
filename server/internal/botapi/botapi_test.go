package botapi

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestProbeMessengerBot(t *testing.T) {
	const token = "123:BOT_TOKEN_FOR_TEST"
	for _, tc := range []struct {
		platform, host, path, authorization, body string
	}{
		{Telegram, "api.telegram.org", "/bot" + token + "/getMe", "",
			`{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"Бот","username":"example_bot"}}`},
		{MAX, "platform-api2.max.ru", "/me", token,
			`{"user_id":42,"is_bot":true,"first_name":"Бот","username":"example_bot"}`},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != tc.host ||
					r.URL.Path != tc.path || r.URL.RawQuery != "" || r.Header.Get("Authorization") != tc.authorization {
					t.Fatal("неверный запрос сведений о боте")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			bot, state := Probe(context.Background(), tc.platform, token, transport)
			if state != OK || bot.ID != 42 || bot.Name != "Бот" || bot.Username != "example_bot" {
				t.Fatalf("неверный результат: %+v, %s", bot, state)
			}
		})
	}
}

func TestProbeSanitizesMessengerBotFailures(t *testing.T) {
	const token = "123:BOT_TOKEN_FOR_TEST"
	for _, tc := range []struct {
		name   string
		status int
		body   string
		err    error
		state  State
	}{
		{name: "отклонён", status: 401, body: token, state: Denied},
		{name: "сеть", err: errors.New(token), state: Unavailable},
		{name: "TLS", err: &tls.CertificateVerificationError{Err: errors.New(token)}, state: Untrusted},
		{name: "перенаправление", status: 302, state: Unavailable},
		{name: "сломанный ответ", status: 200, body: token, state: BadReply},
		{name: "не бот", status: 200, body: `{"ok":true,"result":{"id":42,"is_bot":false}}`, state: BadReply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": {"https://other.invalid"}},
					Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			bot, state := Probe(context.Background(), Telegram, token, transport)
			if calls != 1 || state != tc.state || bot != (Identity{}) || strings.Contains(state.Message(), token) {
				t.Fatalf("неверное состояние проверки: %s", state)
			}
		})
	}
}
