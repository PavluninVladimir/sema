package botapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const TelegramWebhookSecretHeader = "X-Telegram-Bot-Api-Secret-Token"
const TelegramCloudURL = "https://api.telegram.org"

type TelegramClient struct {
	Token     string
	BaseURL   string
	Transport http.RoundTripper
}

// NormalizeTelegramAPIURL принимает адрес сервера без пути, токена и параметров.
func NormalizeTelegramAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	base, err := url.Parse(raw)
	if err != nil || base.Hostname() == "" || base.User != nil || strings.ContainsAny(raw, "?#") ||
		(base.Path != "" && base.EscapedPath() != "/") || (base.Scheme != "https" && base.Scheme != "http") {
		return "", errors.New("укажите адрес сервера Bot API без токена, пути и параметров")
	}
	if base.Scheme == "http" {
		host := strings.TrimSuffix(strings.ToLower(base.Hostname()), ".")
		ip, parseErr := netip.ParseAddr(host)
		local := strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".lan") ||
			!strings.ContainsAny(host, ".:")
		if parseErr == nil {
			local = ip.Unmap().IsLoopback() || ip.Unmap().IsPrivate()
		}
		if !local {
			return "", errors.New("для сервера Bot API вне локальной сети нужен HTTPS")
		}
	}
	return base.Scheme + "://" + base.Host, nil
}

func telegramMethodURL(base, token, method string) (string, error) {
	base, err := NormalizeTelegramAPIURL(base)
	if err != nil {
		return "", err
	}
	if base == "" {
		base = TelegramCloudURL
	}
	return base + "/bot" + token + "/" + method, nil
}

type TelegramWebhookInfo struct {
	URL       string `json:"url"`
	Pending   int    `json:"pending_update_count"`
	ErrorDate int64  `json:"last_error_date"`
}

func (c TelegramClient) WebhookInfo(ctx context.Context) (TelegramWebhookInfo, error) {
	return telegramCall[TelegramWebhookInfo](ctx, c, "getWebhookInfo", struct{}{})
}

func (c TelegramClient) SetWebhook(ctx context.Context, url, secret string) error {
	return telegramBool(ctx, c, "setWebhook", map[string]any{
		"url": url, "secret_token": secret, "max_connections": 4,
		"allowed_updates": []string{"message", "edited_message"},
	})
}

func (c TelegramClient) DeleteWebhook(ctx context.Context) error {
	return telegramBool(ctx, c, "deleteWebhook", struct{}{})
}

func (c TelegramClient) SendText(ctx context.Context, chat, thread int64, text string) (int64, error) {
	result, err := telegramCall[struct {
		ID int64 `json:"message_id"`
	}](ctx, c, "sendMessage", struct {
		Chat   int64  `json:"chat_id"`
		Thread int64  `json:"message_thread_id,omitempty"`
		Text   string `json:"text"`
	}{chat, thread, text})
	return result.ID, err
}

func (c TelegramClient) DeleteMessage(ctx context.Context, chat, message int64) error {
	return telegramBool(ctx, c, "deleteMessage", map[string]int64{"chat_id": chat, "message_id": message})
}

func (c TelegramClient) CanModerateAuthor(ctx context.Context, chat, user int64) (bool, error) {
	result, err := telegramCall[struct {
		Status string `json:"status"`
	}](ctx, c, "getChatMember", map[string]int64{"chat_id": chat, "user_id": user})
	if err != nil {
		return false, err
	}
	switch result.Status {
	case "member", "restricted", "left", "kicked":
		return true, nil
	}
	return false, nil
}

func telegramBool(ctx context.Context, client TelegramClient, method string, payload any) error {
	result, err := telegramCall[bool](ctx, client, method, payload)
	if err == nil && !result {
		return fmt.Errorf("Telegram не подтвердил %s", method)
	}
	return err
}

func telegramCall[T any](ctx context.Context, bot TelegramClient, method string, payload any) (T, error) {
	var zero T
	if len(bot.Token) > 4096 || !telegramToken.MatchString(bot.Token) {
		return zero, errors.New(BadToken.Message())
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return zero, errors.New("не удалось подготовить запрос Telegram")
	}
	endpoint, err := telegramMethodURL(bot.BaseURL, bot.Token, method)
	if err != nil {
		return zero, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return zero, errors.New("не удалось подготовить запрос Telegram")
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{
		Transport: bot.Transport, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		return zero, fmt.Errorf("Telegram не ответил на %s", method)
	}
	defer res.Body.Close()
	const maxReply = 128 << 10
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxReply+1))
	if err != nil || len(raw) > maxReply {
		return zero, fmt.Errorf("не удалось прочитать ответ Telegram на %s", method)
	}
	var response struct {
		OK     bool `json:"ok"`
		Code   int  `json:"error_code"`
		Result T    `json:"result"`
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return zero, fmt.Errorf("Telegram отклонил %s (код %d)", method, res.StatusCode)
	}
	if json.Unmarshal(raw, &response) != nil {
		return zero, fmt.Errorf("Telegram вернул неожиданный ответ на %s", method)
	}
	if !response.OK {
		return zero, fmt.Errorf("Telegram отклонил %s (код %d)", method, response.Code)
	}
	return response.Result, nil
}

type TelegramUpdate struct {
	ID     int64            `json:"update_id"`
	New    *TelegramMessage `json:"message"`
	Edited *TelegramMessage `json:"edited_message"`
}

type TelegramUser struct {
	ID  int64 `json:"id"`
	Bot bool  `json:"is_bot"`
}

type TelegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type TelegramEntity struct {
	Type string `json:"type"`
}

type TelegramMessage struct {
	ID              int64            `json:"message_id"`
	Thread          int64            `json:"message_thread_id"`
	Date            int64            `json:"date"`
	Chat            TelegramChat     `json:"chat"`
	From            TelegramUser     `json:"from"`
	SenderChat      *TelegramChat    `json:"sender_chat"`
	Text            string           `json:"text"`
	Caption         string           `json:"caption"`
	Entities        []TelegramEntity `json:"entities"`
	CaptionEntities []TelegramEntity `json:"caption_entities"`
	NewMembers      []TelegramUser   `json:"new_chat_members"`
}
