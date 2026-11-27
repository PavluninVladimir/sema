// Package botapi проверяет подключения к API ботов.
package botapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	Telegram = "telegram"
	MAX      = "max"
)

type Identity struct {
	ID       int64
	Name     string
	Username string
}

type State string

const (
	OK          State = "ok"
	Denied      State = "denied"
	Unavailable State = "unavailable"
	BadReply    State = "bad_reply"
	BadToken    State = "bad_token"
	Untrusted   State = "untrusted"
)

func (s State) Message() string {
	switch s {
	case OK:
		return "Токен подтверждён"
	case Denied:
		return "Мессенджер отклонил токен"
	case BadReply:
		return "Мессенджер вернул неожиданные сведения о боте"
	case BadToken:
		return "Проверьте формат токена бота"
	case Untrusted:
		return "Сервер Сёмы не доверяет сертификату API мессенджера"
	default:
		return "Не удалось связаться с API мессенджера. Повторите проверку позже."
	}
}

var telegramToken = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

// Probe возвращает только фиксированные состояния: ошибка HTTP может
// содержать URL Telegram с токеном, а тело отказа приходит снаружи.
func Probe(ctx context.Context, platform, token string, transport http.RoundTripper) (Identity, State) {
	return probe(ctx, platform, token, "", transport)
}

func ProbeTelegram(ctx context.Context, token, baseURL string, transport http.RoundTripper) (Identity, State) {
	return probe(ctx, Telegram, token, baseURL, transport)
}

func probe(ctx context.Context, platform, token, baseURL string, transport http.RoundTripper) (Identity, State) {
	if token == "" || len(token) > 4096 || strings.IndexFunc(token, func(r rune) bool { return r < '!' || r > '~' }) >= 0 {
		return Identity{}, BadToken
	}
	var endpoint string
	switch platform {
	case Telegram:
		if !telegramToken.MatchString(token) {
			return Identity{}, BadToken
		}
		var err error
		endpoint, err = telegramMethodURL(baseURL, token, "getMe")
		if err != nil {
			return Identity{}, Unavailable
		}
	case MAX:
		endpoint = "https://platform-api2.max.ru/me"
	default:
		return Identity{}, BadReply
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Identity{}, BadToken
	}
	if platform == MAX {
		req.Header.Set("Authorization", token)
	}
	client := http.Client{Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return Identity{}, Untrusted
		}
		return Identity{}, Unavailable
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden ||
		(platform == Telegram && res.StatusCode == http.StatusNotFound) {
		return Identity{}, Denied
	}
	if res.StatusCode != http.StatusOK {
		return Identity{}, Unavailable
	}
	const maxReply = 64 << 10
	body, err := io.ReadAll(io.LimitReader(res.Body, maxReply+1))
	if err != nil {
		return Identity{}, Unavailable
	}
	if len(body) > maxReply {
		return Identity{}, BadReply
	}
	bot, state := decodeIdentity(platform, body)
	if state == OK {
		bot.Name = strings.TrimSpace(strings.ReplaceAll(bot.Name, token, "•••"))
		bot.Username = strings.TrimSpace(strings.ReplaceAll(bot.Username, token, "•••"))
	}
	return bot, state
}

type botUser struct {
	TelegramID int64  `json:"id"`
	MAXID      int64  `json:"user_id"`
	FirstName  string `json:"first_name"`
	Username   string `json:"username"`
	IsBot      bool   `json:"is_bot"`
}

func decodeIdentity(platform string, body []byte) (Identity, State) {
	var user botUser
	if platform == Telegram {
		var reply struct {
			OK        bool    `json:"ok"`
			ErrorCode int     `json:"error_code"`
			Result    botUser `json:"result"`
		}
		if json.Unmarshal(body, &reply) != nil {
			return Identity{}, BadReply
		}
		if !reply.OK {
			switch reply.ErrorCode {
			case 401, 403, 404:
				return Identity{}, Denied
			case 429:
				return Identity{}, Unavailable
			}
			return Identity{}, BadReply
		}
		user = reply.Result
	} else if json.Unmarshal(body, &user) != nil {
		return Identity{}, BadReply
	}
	id := user.MAXID
	if platform == Telegram {
		id = user.TelegramID
	}
	if id <= 0 || !user.IsBot {
		return Identity{}, BadReply
	}
	return Identity{ID: id, Name: user.FirstName, Username: user.Username}, OK
}
