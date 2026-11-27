package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/botapi"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

var telegramURLText = regexp.MustCompile(`(?i)(?:https?://|tg://|\b(?:www\.|t\.me/|telegram\.me/))`)

func (s *Server) handleTelegramBotUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	secret := r.Header.Get(botapi.TelegramWebhookSecretHeader)
	if err != nil || id <= 0 || len(secret) != 43 {
		http.NotFound(w, r)
		return
	}
	st := s.eng.Store()
	state, err := st.TelegramBotStateFor(id)
	if err != nil {
		http.Error(w, "попробуйте позже", http.StatusServiceUnavailable)
		return
	}
	hash := sha256.Sum256([]byte(secret))
	if state.WebhookURL == "" || subtle.ConstantTimeCompare(hash[:], state.WebhookHash) != 1 {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	var update botapi.TelegramUpdate
	if err != nil || json.Unmarshal(body, &update) != nil || update.ID <= 0 {
		http.Error(w, "неверное обновление Telegram", http.StatusBadRequest)
		return
	}
	message := update.New
	if message == nil {
		message = update.Edited
	}
	var chat, sender int64
	if message != nil && message.Chat.ID < 0 && (message.Chat.Type == "group" || message.Chat.Type == "supergroup") {
		chat = message.Chat.ID
	}
	group := chat != 0 && slices.Contains(state.Features.Groups, chat)
	countFlood := group && state.Features.BlockFlood && update.New != nil && message.From.ID > 0 &&
		!message.From.Bot && len(message.NewMembers) == 0 && message.SenderChat == nil
	if countFlood {
		sender = message.From.ID
	}
	claimed, recent, err := st.ClaimTelegramBotDelivery(id, update.ID, chat, sender, hash[:])
	if err != nil {
		http.Error(w, "попробуйте позже", http.StatusServiceUnavailable)
		return
	}
	if !claimed || message == nil || message.ID <= 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	bot, err := st.MessengerBot(id)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	change := telegramBotResponse(state.Features, bot.Username, message, group, update.New != nil, recent)
	if change.text != "" || change.spam != "" {
		_ = st.SetTelegramBotResult(id, s.applyTelegramBotResponse(r, bot, message, change))
	}
	w.WriteHeader(http.StatusNoContent)
}

type telegramResponse struct {
	text string
	spam string
}

func telegramBotResponse(f store.TelegramBotFeatures, username string, m *botapi.TelegramMessage, group, fresh bool, recent int) telegramResponse {
	private := m.Chat.Type == "private"
	if m.From.Bot || m.From.ID <= 0 || (!group && !private) {
		return telegramResponse{}
	}
	if group && f.Welcome && fresh {
		for _, member := range m.NewMembers {
			if !member.Bot && member.ID > 0 {
				return telegramResponse{text: telegramText(f.WelcomeText, "Добро пожаловать!")}
			}
		}
	}
	if group && m.SenderChat == nil {
		if reason := telegramSpam(f, m, recent); reason != "" {
			return telegramResponse{spam: reason}
		}
	}
	if !fresh || len(m.NewMembers) != 0 {
		return telegramResponse{}
	}
	command := strings.Fields(m.Text)
	if len(command) != 0 && strings.HasPrefix(command[0], "/") {
		if f.Commands {
			return telegramResponse{text: telegramCommand(f, command[0], username)}
		}
		return telegramResponse{}
	}
	if private && f.PrivateReply && (m.Text != "" || m.Caption != "") {
		return telegramResponse{text: telegramText(f.PrivateReplyText, "Спасибо за сообщение.")}
	}
	return telegramResponse{}
}

func telegramSpam(f store.TelegramBotFeatures, message *botapi.TelegramMessage, recent int) string {
	text := message.Text + " " + message.Caption
	if f.BlockLinks {
		for _, entities := range [][]botapi.TelegramEntity{message.Entities, message.CaptionEntities} {
			for _, entity := range entities {
				if entity.Type == "url" || entity.Type == "text_link" {
					return "ссылка"
				}
			}
		}
		if telegramURLText.MatchString(text) {
			return "ссылка"
		}
	}
	if f.BlockWords {
		normalized := " " + normalizeTelegramWords(text) + " "
		for _, word := range f.Words {
			if strings.Contains(normalized, " "+word+" ") {
				return "запрещённая фраза"
			}
		}
	}
	if f.BlockFlood && recent >= 6 {
		return "флуд"
	}
	return ""
}

func telegramCommand(f store.TelegramBotFeatures, input, username string) string {
	command, to, addressed := strings.Cut(input, "@")
	if addressed && !strings.EqualFold(to, username) {
		return ""
	}
	switch strings.ToLower(command) {
	case "/start":
		return telegramText(f.StartText, "Привет! Напишите /help, чтобы посмотреть команды.")
	case "/help":
		return telegramText(f.HelpText, "/start — начать разговор.\n/help — показать справку.")
	}
	return ""
}

func telegramText(text, fallback string) string {
	if text != "" {
		return text
	}
	return fallback
}

func (s *Server) applyTelegramBotResponse(r *http.Request, bot store.MessengerBot, m *botapi.TelegramMessage, change telegramResponse) string {
	client, err := s.telegramBotClient(bot)
	if err != nil {
		return "Токен бота недоступен"
	}
	ctx := r.Context()
	if change.spam == "" {
		if _, err := client.SendText(ctx, m.Chat.ID, m.Thread, change.text); err != nil {
			return err.Error()
		}
		return "Бот отправил ответ"
	}
	mayDelete, err := client.CanModerateAuthor(ctx, m.Chat.ID, m.From.ID)
	if err != nil {
		return "Не удалось проверить права автора · " + err.Error()
	}
	if !mayDelete {
		return "Сообщение администратора пропущено"
	}
	if err := client.DeleteMessage(ctx, m.Chat.ID, m.ID); err != nil {
		return "Не удалось удалить спам · " + err.Error()
	}
	return "Спам удалён · " + change.spam
}
