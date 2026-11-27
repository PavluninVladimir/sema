package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func (s *Server) telegramBotSections(bot store.MessengerBot) []Component {
	state, err := s.eng.Store().TelegramBotStateFor(bot.ID)
	if err != nil {
		return []Component{emptyState("Настройки бота недоступны", "Повторите попытку позже.", "tray")}
	}
	id := strconv.FormatInt(bot.ID, 10)
	address := s.telegramWebhookURL(bot.ID)
	if state.WebhookURL != "" {
		address = state.WebhookURL
	}
	status := "Выключен"
	if state.WebhookURL != "" {
		status = "Включён"
	}
	urlRow := Component{Type: "row", Layout: "wide", Subtitle: "Webhook URL", Caption: address}
	if address != "" {
		urlRow.Actions = []Action{{Action: "$copy", Target: address, Label: "Скопировать URL"}}
	} else {
		urlRow.Caption = "Укажите публичный HTTPS-адрес Сёмы в «Вход и доступ»."
		urlRow.Actions = []Action{{Action: "$goto", Target: accessScreen, Label: "Открыть настройки доступа"}}
	}
	check := Component{Type: "row", Layout: "wide", Subtitle: "Состояние", Caption: status,
		Actions: []Action{{Action: "check_telegram_bot_webhook", Target: id, Label: "Проверить webhook"}}}
	if state.LastDelivery > 0 {
		check.Caption += " · последняя доставка " + whenText(time.UnixMilli(state.LastDelivery).UTC().Format(time.RFC3339))
	}
	if state.LastResult != "" {
		check.Caption += " · " + state.LastResult
	}
	webhook := Component{Type: "section", ID: "telegram-bot-webhook", Title: "Webhook Telegram", Children: []Component{
		urlRow, check,
		{Type: "form", ID: "bot-webhook-form", Style: formRows,
			Fields: []Field{{Type: "toggle", Name: "webhook", Label: "Включить webhook",
				Hint:  "Сёма зарегистрирует URL в Telegram. Пока webhook включён, getUpdates недоступен.",
				Value: boolValue(state.WebhookURL != "")}},
			Submit: &Action{Action: "set_telegram_bot_webhook", Target: id, Label: "Применить"}},
	}}
	f := state.Features
	groupIDs := make([]string, 0, len(f.Groups))
	for _, chat := range f.Groups {
		groupIDs = append(groupIDs, strconv.FormatInt(chat, 10))
	}
	groupHint := "Отрицательные ID через запятую. При пустом списке функции в группах выключены. Чтобы узнать ID, отправьте в группе команду боту."
	if state.LastGroup != 0 {
		groupHint += " ID последней группы: " + strconv.FormatInt(state.LastGroup, 10) + "."
	}
	features := Component{Type: "form", ID: "telegram-bot-features", Title: "Функции бота", Style: formRows,
		Fields: []Field{
			{Type: "text", Name: "groups", Label: "Разрешённые группы", Value: strings.Join(groupIDs, ", "),
				Hint: groupHint},
			{Type: "toggle", Name: "block_links", Label: "Удалять сообщения со ссылками", Value: boolValue(f.BlockLinks),
				Hint: "Антиспам для выбранных групп. Боту нужны права администратора на удаление сообщений."},
			{Type: "toggle", Name: "block_words", Label: "Удалять сообщения по списку слов", Value: boolValue(f.BlockWords)},
			{Type: "text", Name: "words", Label: "Запрещённые слова и фразы", Value: strings.Join(f.Words, ", "),
				Hint: "Разделите слова и фразы запятыми."},
			{Type: "toggle", Name: "block_flood", Label: "Защищать от флуда", Value: boolValue(f.BlockFlood),
				Hint: "Удаляет сообщения участника начиная с шестого за 10 секунд."},
			{Type: "toggle", Name: "welcome", Label: "Приветствовать новых участников", Value: boolValue(f.Welcome)},
			{Type: "text", Name: "welcome_text", Label: "Текст приветствия", Value: f.WelcomeText, Hint: "По умолчанию «Добро пожаловать!»."},
			{Type: "toggle", Name: "commands", Label: "Отвечать на /start и /help", Value: boolValue(f.Commands)},
			{Type: "text", Name: "start_text", Label: "Ответ на /start", Value: f.StartText},
			{Type: "text", Name: "help_text", Label: "Ответ на /help", Value: f.HelpText},
			{Type: "toggle", Name: "private_reply", Label: "Автоответ в личных сообщениях", Value: boolValue(f.PrivateReply)},
			{Type: "text", Name: "private_reply_text", Label: "Текст автоответа", Value: f.PrivateReplyText},
		},
		Submit: &Action{Action: "save_telegram_bot_features", Target: id, Label: "Сохранить функции"},
	}
	apiURL := s.telegramBotAPIURL(bot.ID)
	api := Component{Type: "section", ID: "telegram-bot-api", Title: "API Сёмы для ваших сервисов", Children: []Component{
		{Type: "row", Layout: "wide", Subtitle: "URL управления ботом", Caption: apiURL,
			Meta:    "GET читает настройки. PATCH меняет webhook и функции.",
			Actions: []Action{{Action: "$copy", Target: apiURL, Label: "Скопировать URL API"}}},
		{Type: "row", Layout: "wide", Subtitle: "Отправка сообщений", Caption: apiURL + "/send",
			Meta:    "POST принимает chat_id, text и необязательный message_thread_id.",
			Actions: []Action{{Action: "$copy", Target: apiURL + "/send", Label: "Скопировать URL отправки"}}},
		{Type: "row", Layout: "wide", Subtitle: "Токен Сёмы", Caption: "Authorization: Bearer <токен Сёмы>",
			Meta:    "Для сервисного токена оставьте только право «Управление ботами Telegram».",
			Actions: []Action{{Action: "$goto", Target: accessScreen + "~" + accessTabTokens, Label: "Открыть токены"}}},
	}}
	return []Component{webhook, api, features}
}

func (s *Server) telegramBotSettingsAction(req actionRequest) (string, error) {
	addr := entityAddr(botsScreen, req.Target, "")
	ctx := req.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	switch req.Action {
	case "check_telegram_bot_webhook":
		return addr, s.checkTelegramWebhook(ctx, req.Target)
	case "set_telegram_bot_webhook":
		value := req.Params["webhook"]
		if value != "true" && value != "false" {
			return "", errors.New("выберите состояние webhook")
		}
		enabled := value == "true"
		_, err := s.updateTelegramBot(ctx, req.Target, telegramBotPatch{Webhook: &enabled})
		return addr, err
	case "save_telegram_bot_features":
		p := req.Params
		var groups []int64
		for _, part := range strings.FieldsFunc(p["groups"], func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) }) {
			group, err := strconv.ParseInt(part, 10, 64)
			if err != nil || group >= 0 {
				return "", errors.New("укажите отрицательные ID групп через запятую")
			}
			groups = append(groups, group)
		}
		f := store.TelegramBotFeatures{
			Groups: groups, Words: strings.FieldsFunc(p["words"], func(r rune) bool { return r == ',' || r == '\n' }),
			BlockLinks: p["block_links"] == "true", BlockWords: p["block_words"] == "true", BlockFlood: p["block_flood"] == "true",
			Welcome: p["welcome"] == "true", WelcomeText: p["welcome_text"],
			Commands: p["commands"] == "true", StartText: p["start_text"], HelpText: p["help_text"],
			PrivateReply: p["private_reply"] == "true", PrivateReplyText: p["private_reply_text"],
		}
		raw, err := json.Marshal(f)
		if err != nil {
			return "", err
		}
		_, err = s.updateTelegramBot(ctx, req.Target, telegramBotPatch{Features: raw})
		return addr, err
	}
	return "", errUnhandled
}
