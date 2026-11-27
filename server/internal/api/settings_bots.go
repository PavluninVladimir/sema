package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/botapi"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	botsScreen = "settings:bots"
	botsTitle  = "Управление ботами"
)

func botPlatformTitle(platform string) string {
	if platform == botapi.MAX {
		return "MAX"
	}
	return "Telegram"
}

func botsListAddr(platform string) string {
	if platform == botapi.MAX {
		return entityAddr(botsScreen, "", botapi.MAX)
	}
	return botsScreen
}

func botScreenAddr(id int64) string {
	return entityAddr(botsScreen, strconv.FormatInt(id, 10), "")
}

func botTitle(bot store.MessengerBot) string {
	if bot.Name != "" {
		return bot.Name
	}
	if bot.Username != "" {
		return "@" + bot.Username
	}
	return "Бот " + strconv.FormatInt(bot.ExternalID, 10)
}

func botCaption(bot store.MessengerBot) string {
	caption := botPlatformTitle(bot.Platform) + " · ID " + strconv.FormatInt(bot.ExternalID, 10)
	if bot.Username != "" {
		caption += " · @" + bot.Username
	}
	return caption
}

func botBadge(bot store.MessengerBot) Badge {
	state := botapi.State(bot.Status)
	if state == botapi.OK {
		return Badge{Text: "Токен подтверждён", Level: LevelOK}
	}
	return Badge{Text: "Проверка не прошла", Level: LevelWarn}
}

func (s *Server) buildMessengerBots(doc *Document, screen string) error {
	if doc.guest {
		return errors.New("управление ботами доступно только владельцу")
	}
	_, id, tab := splitEntity(screen)
	platform := botapi.Telegram
	if tab == botapi.MAX {
		platform = botapi.MAX
	}
	switch id {
	case "new":
		return s.buildNewMessengerBot(doc, platform)
	case "":
		return s.buildMessengerBotList(doc, platform)
	}
	bot, err := s.messengerBot(id)
	if err != nil {
		doc.Heading = botsTitle
		doc.Components = append(doc.Components, emptyState("Подключение недоступно", err.Error(), "tray"))
		doc.ToolbarActions = []Action{{Action: "$goto", Target: botsListAddr(platform), Label: "К списку ботов"}}
		return nil
	}
	s.buildMessengerBot(doc, bot)
	return nil
}

func (s *Server) buildMessengerBotList(doc *Document, platform string) error {
	doc.Heading, doc.Layout = botsTitle, "wide"
	setSubtabs(doc, botsScreen, platform, []innerTab{
		{ID: botapi.Telegram, Addr: botsScreen, Name: "Telegram"}, {ID: botapi.MAX, Name: "MAX"},
	})
	add := Action{Action: "$goto", Target: entityAddr(botsScreen, "new", platform), Label: "Добавить бота", Style: "primary"}
	bots, err := s.eng.Store().MessengerBots()
	if err != nil {
		return err
	}
	var rows []Component
	for _, bot := range bots {
		if bot.Platform != platform {
			continue
		}
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "bot-" + strconv.FormatInt(bot.ID, 10),
			Subtitle: botTitle(bot), Caption: botCaption(bot), Badges: []Badge{botBadge(bot)},
			Actions: []Action{{Action: "$goto", Target: botScreenAddr(bot.ID), Label: "Открыть"}}})
	}
	if len(rows) == 0 {
		empty := emptyState("Ботов пока нет", "Добавьте существующего бота "+botPlatformTitle(platform)+" по токену.", "tray")
		empty.Actions = []Action{add}
		rows = append(rows, empty)
	} else {
		doc.ToolbarActions = []Action{add}
	}
	doc.Components = append(doc.Components, Component{Type: "section", Children: rows})
	return nil
}

func (s *Server) buildNewMessengerBot(doc *Document, platform string) error {
	doc.Heading, doc.Layout = "Добавить бота "+botPlatformTitle(platform), "read"
	doc.Components = append(doc.Components, Component{Type: "crumb", Actions: []Action{{Action: "$goto",
		Target: botsListAddr(platform), Label: "‹ " + botsTitle}}})
	if !s.eng.Store().SecretsEncrypted() {
		doc.Components = append(doc.Components, emptyState("Нужен ключ шифрования",
			"Настройте ключ шифрования Сёмы, чтобы сохранить токен.", "lock"))
		return nil
	}
	hint := "Возьмите токен бота в @BotFather."
	if platform == botapi.MAX {
		hint = "Возьмите токен в настройках бота на платформе MAX."
	}
	fields := []Field{{Type: "secure", Name: "token", Label: "Токен бота", Hint: hint}}
	if platform == botapi.Telegram {
		fields = append(fields, telegramBotServerFields("")...)
	}
	doc.Components = append(doc.Components, Component{Type: "form", ID: "bot-new", Style: formRows, Fields: fields,
		Submit: &Action{Action: "add_messenger_bot", Target: platform, Label: "Подключить", Style: "primary"}})
	return nil
}

func (s *Server) buildMessengerBot(doc *Document, bot store.MessengerBot) {
	doc.Layout = "read"
	id := strconv.FormatInt(bot.ID, 10)
	check := Action{Action: "check_messenger_bot", Target: id, Label: "Проверить подключение"}
	badge := botBadge(bot)
	status := whenText(bot.CheckedAt)
	if bot.Status != string(botapi.OK) {
		status += " · " + botapi.State(bot.Status).Message()
	}
	entityFrame(doc, entity{Section: botsScreen, ID: id, Name: botTitle(bot), Caption: botCaption(bot),
		Back: botsTitle, BackTo: botsListAddr(bot.Platform), Primary: &check, Level: badge.Level, Badges: []Badge{badge}})
	doc.Components = append(doc.Components, Component{Type: "row", Layout: "wide", Subtitle: "Последняя проверка", Caption: status})
	if bot.Platform == botapi.Telegram {
		doc.Components = append(doc.Components, s.telegramBotSections(bot)...)
	}
	doc.Components = append(doc.Components,
		Component{Type: "form", ID: "bot-token", Style: formRows, Title: "Замена токена",
			Fields: []Field{{Type: "secure", Name: "token", Label: "Новый токен"}},
			Submit: &Action{Action: "replace_messenger_bot_token", Target: id, Label: "Заменить токен"}},
		Component{Type: "section", ID: "bot-danger", Title: "Опасное", Style: "danger", Children: []Component{{
			Type: "row", Layout: "wide", Subtitle: "Отключить бота от Сёмы", Actions: []Action{{
				Action: "delete_messenger_bot", Target: id, Label: "Отключить…", Style: "destructive",
				Confirm: "Удалить подключение и сохранённый токен из Сёмы?", ConfirmOK: "Отключить",
			}},
		}}})
}

func (s *Server) messengerBot(id string) (store.MessengerBot, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return store.MessengerBot{}, errors.New("неверный идентификатор подключения")
	}
	return s.eng.Store().MessengerBot(n)
}

func (s *Server) messengerBotActions(req actionRequest) (string, error) {
	switch req.Action {
	case "add_messenger_bot":
		return s.addMessengerBot(req)
	case "set_telegram_bot_webhook", "check_telegram_bot_webhook", "save_telegram_bot_features", "set_telegram_bot_server":
		return s.telegramBotSettingsAction(req)
	case "check_messenger_bot", "replace_messenger_bot_token", "delete_messenger_bot":
	default:
		return "", errUnhandled
	}
	s.botSettingsMu.Lock()
	defer s.botSettingsMu.Unlock()
	bot, err := s.messengerBot(req.Target)
	if err != nil {
		return "", err
	}
	st := s.eng.Store()
	if req.Action == "delete_messenger_bot" {
		if bot.Platform == botapi.Telegram {
			state, err := st.TelegramBotStateFor(bot.ID)
			if err != nil {
				return "", err
			}
			ctx := req.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			if err := s.configureTelegramWebhook(ctx, bot, &state, false); err != nil {
				return "", err
			}
		}
		return botsListAddr(bot.Platform), st.DeleteMessengerBot(bot)
	}
	token := strings.TrimSpace(req.Params["token"])
	replace := req.Action == "replace_messenger_bot_token"
	if !replace {
		token, err = st.MessengerBotToken(bot)
		if err != nil {
			return "", err
		}
	}
	identity, state := s.probeMessengerBot(req, bot, token)
	if state == botapi.OK && identity.ID != bot.ExternalID {
		if replace {
			return "", errors.New("этот токен принадлежит другому боту")
		}
		state = botapi.BadReply
	}
	applyBotCheck(&bot, identity, state)
	if replace {
		if state != botapi.OK {
			return "", errors.New(state.Message())
		}
		return botScreenAddr(bot.ID), st.ReplaceMessengerBotToken(bot, token)
	}
	return botScreenAddr(bot.ID), st.SaveMessengerBotCheck(bot)
}

func (s *Server) addMessengerBot(req actionRequest) (string, error) {
	platform, token := req.Target, strings.TrimSpace(req.Params["token"])
	if platform != botapi.Telegram && platform != botapi.MAX {
		return "", errors.New("выберите Telegram или MAX")
	}
	st := s.eng.Store()
	if !st.SecretsEncrypted() {
		return "", errors.New("для сохранения токена нужен ключ шифрования Сёмы")
	}
	bot := store.MessengerBot{Platform: platform}
	if platform == botapi.Telegram {
		baseURL, err := telegramBotServerFromForm(req.Params)
		if err != nil {
			return "", err
		}
		bot.TelegramAPIURL = baseURL
	}
	identity, state := s.probeMessengerBot(req, bot, token)
	if state != botapi.OK {
		return "", errors.New(state.Message())
	}
	bot.ExternalID = identity.ID
	applyBotCheck(&bot, identity, state)
	id, err := st.AddMessengerBot(bot, token)
	if err != nil {
		return "", fmt.Errorf("не удалось сохранить подключение: %w", err)
	}
	return botScreenAddr(id), nil
}

func (s *Server) probeMessengerBot(req actionRequest, bot store.MessengerBot, token string) (botapi.Identity, botapi.State) {
	ctx := req.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if bot.Platform == botapi.Telegram {
		return botapi.ProbeTelegram(ctx, token, bot.TelegramAPIURL, s.botTransport)
	}
	return botapi.Probe(ctx, bot.Platform, token, s.botTransport)
}

func applyBotCheck(bot *store.MessengerBot, identity botapi.Identity, state botapi.State) {
	bot.Status, bot.CheckedAt = string(state), time.Now().UTC().Format(time.RFC3339)
	if state == botapi.OK {
		bot.Name, bot.Username = identity.Name, identity.Username
	}
}

func botSettingsLink(platform string) Action {
	return Action{Action: "$goto", Target: botsListAddr(platform), Label: botsTitle}
}
