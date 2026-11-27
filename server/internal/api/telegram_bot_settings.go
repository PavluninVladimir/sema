package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/PavluninVladimir/sema/server/internal/botapi"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const telegramWebhookPath = "/api/webhooks/telegram/"

func (s *Server) telegramBotAPIURL(id int64) string {
	path := "/api/bots/" + strconv.FormatInt(id, 10) + "/telegram"
	origin, err := url.Parse(s.auth.Origin())
	if err != nil || origin.Host == "" || origin.User != nil || (origin.Scheme != "http" && origin.Scheme != "https") {
		return path
	}
	return origin.Scheme + "://" + origin.Host + path
}

func isTelegramWebhookPath(path string) bool {
	id, ok := strings.CutPrefix(path, telegramWebhookPath)
	return ok && id != "" && id[0] >= '1' && id[0] <= '9' && strings.IndexFunc(id, func(r rune) bool {
		return r < '0' || r > '9'
	}) == -1
}

func (s *Server) telegramWebhookURL(id int64) string {
	origin, err := url.Parse(s.auth.Origin())
	if err != nil || origin.Scheme != "https" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(origin.Hostname(), "."))
	if !strings.Contains(host, ".") && !strings.Contains(host, ":") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".lan") {
		return ""
	}
	if ip, err := netip.ParseAddr(host); err == nil && (!ip.IsGlobalUnicast() || ip.IsPrivate()) {
		return ""
	}
	switch origin.Port() {
	case "", "443", "80", "88", "8443":
	default:
		return ""
	}
	return "https://" + origin.Host + telegramWebhookPath + strconv.FormatInt(id, 10)
}

type telegramBotPatch struct {
	Webhook  *bool           `json:"webhook"`
	Features json.RawMessage `json:"features"`
}

type telegramBotView struct {
	Enabled bool   `json:"webhook"`
	URL     string `json:"url"`
	APIURL  string `json:"api_url"`
	store.TelegramBotState
}

func (s *Server) telegramBotViewFor(botID int64, state store.TelegramBotState) telegramBotView {
	return telegramBotView{Enabled: state.WebhookURL != "", URL: s.telegramWebhookURL(botID),
		APIURL: s.telegramBotAPIURL(botID), TelegramBotState: state}
}

func (s *Server) telegramBot(target string) (store.MessengerBot, error) {
	bot, err := s.messengerBot(target)
	if err != nil || bot.Platform != botapi.Telegram {
		return store.MessengerBot{}, errors.New("бот Telegram не найден")
	}
	return bot, nil
}

func (s *Server) telegramBotClient(bot store.MessengerBot) (botapi.TelegramClient, error) {
	token, err := s.eng.Store().MessengerBotToken(bot)
	return botapi.TelegramClient{Token: token, BaseURL: bot.TelegramAPIURL, Transport: s.botTransport}, err
}

func telegramWebhookSecret(token, url string) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = io.WriteString(mac, "sema.telegram.webhook.v1\n"+url)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// updateTelegramBot сериализует сетевые изменения с заменой и удалением токена.
func (s *Server) updateTelegramBot(ctx context.Context, id string, patch telegramBotPatch) (telegramBotView, error) {
	s.botSettingsMu.Lock()
	defer s.botSettingsMu.Unlock()
	bot, err := s.telegramBot(id)
	if err != nil {
		return telegramBotView{}, err
	}
	st := s.eng.Store()
	state, err := st.TelegramBotStateFor(bot.ID)
	if err != nil {
		return telegramBotView{}, err
	}
	if patch.Features != nil {
		decoder := json.NewDecoder(bytes.NewReader(patch.Features))
		decoder.DisallowUnknownFields()
		if bytes.Equal(bytes.TrimSpace(patch.Features), []byte("null")) || decoder.Decode(&state.Features) != nil {
			return telegramBotView{}, errors.New("проверьте поля features")
		}
		if err := validateTelegramFeatures(&state.Features); err != nil {
			return telegramBotView{}, err
		}
	}
	if patch.Webhook != nil {
		if err := s.configureTelegramWebhook(ctx, bot, &state, *patch.Webhook); err != nil {
			return telegramBotView{}, err
		}
	}
	if err := st.SaveTelegramBotState(bot, state); err != nil {
		return telegramBotView{}, err
	}
	return s.telegramBotViewFor(bot.ID, state), nil
}

func (s *Server) configureTelegramWebhook(ctx context.Context, bot store.MessengerBot, state *store.TelegramBotState, enabled bool) error {
	address := s.telegramWebhookURL(bot.ID)
	if enabled && address == "" {
		return errors.New("укажите публичный HTTPS-адрес Сёмы в «Вход и доступ»")
	}
	if !enabled && state.WebhookURL == "" {
		return nil
	}
	client, err := s.telegramBotClient(bot)
	if err != nil {
		return err
	}
	info, err := client.WebhookInfo(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		if info.URL == state.WebhookURL {
			if err := client.DeleteWebhook(ctx); err != nil {
				return err
			}
		}
		state.WebhookURL, state.WebhookHash = "", nil
		return nil
	}
	if info.URL != "" && info.URL != address && info.URL != state.WebhookURL {
		return errors.New("у бота уже есть webhook другого приложения. Отключите его там перед подключением Сёмы")
	}
	secret := telegramWebhookSecret(client.Token, address)
	if err := client.SetWebhook(ctx, address, secret); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(secret))
	state.WebhookURL, state.WebhookHash = address, digest[:]
	return nil
}

func (s *Server) checkTelegramWebhook(ctx context.Context, id string) error {
	s.botSettingsMu.Lock()
	defer s.botSettingsMu.Unlock()
	bot, err := s.telegramBot(id)
	if err != nil {
		return err
	}
	st := s.eng.Store()
	state, err := st.TelegramBotStateFor(bot.ID)
	if err != nil {
		return err
	}
	client, err := s.telegramBotClient(bot)
	if err != nil {
		return err
	}
	info, err := client.WebhookInfo(ctx)
	if err != nil {
		return err
	}
	// Статус Telegram может относиться к другому приложению. Его адрес и текст ошибки не сохраняем.
	var status string
	switch {
	case state.WebhookURL != "" && info.URL == state.WebhookURL:
		status = fmt.Sprintf("Telegram подтвердил URL · в очереди %d", info.Pending)
		if info.ErrorDate > 0 {
			status += " · последнее сообщение об ошибке " + whenText(time.Unix(info.ErrorDate, 0).UTC().Format(time.RFC3339))
		}
	case info.URL != "":
		status = "Telegram использует другой webhook"
	default:
		status = "Webhook в Telegram не зарегистрирован"
	}
	if err := st.SaveTelegramBotState(bot, state); err != nil {
		return err
	}
	return st.SetTelegramBotResult(bot.ID, status)
}

func validateTelegramFeatures(f *store.TelegramBotFeatures) error {
	if len(f.Groups) > 100 || len(f.Words) > 100 {
		return errors.New("укажите не больше 100 групп и 100 слов или фраз")
	}
	seenGroups := map[int64]bool{}
	groups := make([]int64, 0, len(f.Groups))
	for _, group := range f.Groups {
		if group >= 0 {
			return errors.New("ID группы Telegram должен быть отрицательным числом")
		}
		if !seenGroups[group] {
			groups = append(groups, group)
			seenGroups[group] = true
		}
	}
	f.Groups = groups
	seenWords := map[string]bool{}
	words := make([]string, 0, len(f.Words))
	for _, word := range f.Words {
		if len([]rune(word)) > 100 {
			return errors.New("слово или фраза должны быть не длиннее 100 знаков")
		}
		word = normalizeTelegramWords(word)
		if word != "" && !seenWords[word] {
			words = append(words, word)
			seenWords[word] = true
		}
	}
	f.Words = words
	if f.BlockWords && len(f.Words) == 0 {
		return errors.New("укажите запрещённые слова или фразы")
	}
	for _, text := range []*string{&f.WelcomeText, &f.StartText, &f.HelpText, &f.PrivateReplyText} {
		*text = strings.TrimSpace(*text)
		if !validTelegramText(*text) {
			return errors.New("текст ответа должен быть не длиннее 4096 знаков Telegram")
		}
	}
	return nil
}

func normalizeTelegramWords(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_'
	})
	return strings.Join(words, " ")
}

func validTelegramText(text string) bool {
	return len(text) <= 4*4096 && len(utf16.Encode([]rune(text))) <= 4096
}

func decodeTelegramBotRequest(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(dst) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("проверьте формат JSON запроса")
	}
	return nil
}

func telegramBotHTTPError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": message})
}

func (s *Server) handleTelegramBotSettings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bot, err := s.telegramBot(id)
	if err != nil {
		telegramBotHTTPError(w, http.StatusNotFound, err.Error())
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		state, err := s.eng.Store().TelegramBotStateFor(bot.ID)
		if err != nil {
			telegramBotHTTPError(w, http.StatusInternalServerError, "не удалось прочитать настройки бота")
			return
		}
		writeJSON(w, s.telegramBotViewFor(bot.ID, state))
		return
	}
	var patch telegramBotPatch
	if err := decodeTelegramBotRequest(w, r, &patch); err != nil || (patch.Webhook == nil && patch.Features == nil) {
		telegramBotHTTPError(w, http.StatusBadRequest, "передайте webhook или features")
		return
	}
	state, err := s.updateTelegramBot(r.Context(), id, patch)
	if err != nil {
		telegramBotHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, state)
}

func (s *Server) handleTelegramBotSend(w http.ResponseWriter, r *http.Request) {
	bot, err := s.telegramBot(r.PathValue("id"))
	if err != nil {
		telegramBotHTTPError(w, http.StatusNotFound, err.Error())
		return
	}
	var request struct {
		Chat   int64  `json:"chat_id"`
		Thread int64  `json:"message_thread_id"`
		Text   string `json:"text"`
	}
	if decodeTelegramBotRequest(w, r, &request) != nil || request.Chat == 0 || request.Thread < 0 ||
		strings.TrimSpace(request.Text) == "" || !validTelegramText(request.Text) {
		telegramBotHTTPError(w, http.StatusBadRequest, "укажите chat_id и текст сообщения до 4096 знаков")
		return
	}
	client, err := s.telegramBotClient(bot)
	if err != nil {
		telegramBotHTTPError(w, http.StatusConflict, err.Error())
		return
	}
	message, err := client.SendText(r.Context(), request.Chat, request.Thread, request.Text)
	if err != nil {
		telegramBotHTTPError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]int64{"message_id": message})
}
