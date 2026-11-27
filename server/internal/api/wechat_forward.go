package api

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const wechatMenuID = "chats:wechat"

func wechatChatScreen(peer string) string {
	return wechatScreen + "@" + url.PathEscape(peer)
}

func (s *Server) wechatMenu() *Screen {
	if !s.eng.WeChatConnected() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	chats, err := s.eng.WeChatChats(ctx)
	if err != nil {
		return nil
	}
	node := &Screen{ID: wechatMenuID, Title: "WeChat", Icon: "logo.wechat", Tint: "green", Open: true, Tile: true, NoSelf: true}
	for _, chat := range chats {
		if !chat.Collect || chat.Blocked {
			continue
		}
		icon := "person"
		if strings.HasSuffix(chat.Peer, "@chatroom") {
			icon = "person.2"
		}
		node.Children = append(node.Children, Screen{ID: wechatChatScreen(chat.Peer), Title: wechatChatName(chat), Icon: icon, Tint: "green"})
	}
	if len(node.Children) == 0 {
		return nil
	}
	return node
}

func forwardRecipientLabel(r store.WeChatForwardRecipient) string {
	channel := "Telegram"
	if r.Target.Channel == store.WeChatForwardMAX {
		channel = "MAX"
	}
	label := channel + " · " + r.Title
	if r.Username != "" {
		label += " · @" + strings.TrimPrefix(r.Username, "@")
	}
	if r.AccountTitle != "" {
		label += " · " + r.AccountTitle
	}
	return label
}

const recipientPrefix = "recipient."

func recipientField(target store.WeChatForwardTarget) string {
	// Номер учётки и чата сохраняются в правиле. Названия могут совпасть.
	return recipientPrefix + target.Channel + "." + target.Account + "." + strconv.FormatInt(target.Chat, 10)
}

func (s *Server) buildWeChatForward(ctx context.Context, doc *Document, chat store.WeChatChat) error {
	rule, err := s.eng.Store().WeChatForwardRule(ctx, chat.Account, chat.Peer)
	if err != nil {
		return err
	}
	recipients, err := s.eng.Store().WeChatForwardRecipients(ctx)
	if err != nil {
		return err
	}
	counts, err := s.eng.Store().WeChatForwardCounts(ctx, chat.Account, chat.Peer)
	if err != nil {
		return err
	}
	if rule.SourceLang == "" {
		rule.SourceLang = engine.WeChatForwardOriginal
	}
	if rule.TargetLang == "" {
		rule.TargetLang = s.eng.TranslateTo()
	}
	languages := s.eng.TranslateLangs()
	if !hasLanguage(languages, rule.TargetLang) {
		languages = append(languages, rule.TargetLang)
	}
	sourceOptions := append([]string{engine.WeChatForwardOriginal, engine.WeChatForwardAny}, languages...)
	if !hasLanguage(sourceOptions, rule.SourceLang) {
		sourceOptions = append(sourceOptions, rule.SourceLang)
	}
	fields := []Field{
		{Type: "toggle", Name: "enabled", Label: "Пересылать новые входящие сообщения", Value: boolValue(rule.Enabled)},
		{Type: "picker", Name: "source_language", Label: "Переводить с", Value: rule.SourceLang, Options: sourceOptions},
		{Type: "picker", Name: "target_language", Label: "Переводить на", Value: rule.TargetLang, Options: languages,
			Hint: "Переводит доверенная локальная модель. Текст на других языках сохраняется."},
	}
	available := 0
	var otherRecipients []Field
	for _, recipient := range recipients {
		selected := slices.Contains(rule.Targets, recipient.Target)
		field := Field{Type: "toggle", Name: recipientField(recipient.Target), Label: "Получатель · " + forwardRecipientLabel(recipient), Value: boolValue(selected)}
		if selected {
			available++
			fields = append(fields, field)
		} else {
			otherRecipients = append(otherRecipients, field)
		}
	}
	fields = append(fields, otherRecipients...)
	doc.Components = append(doc.Components, Component{
		Type: "form", ID: "wechat-forward", Title: "Пересылка сообщений", Fields: fields,
		Text:   "Выбранные получатели будут получать новые входящие текстовые сообщения этого чата от вашего имени.",
		Submit: &Action{Action: "wechat_save_forward", Target: chat.Peer, Label: "Сохранить правило", Style: "primary"},
	})
	if len(recipients) == 0 || available < len(rule.Targets) {
		doc.Components = append(doc.Components, Component{Type: "row",
			Subtitle: "Для получателя включите право «Писать» и разрешите отправку от имени аккаунта",
			Actions:  []Action{{Action: "$goto", Target: tgScreen, Label: "Настройки Telegram"}, {Action: "$goto", Target: maxScreen, Label: "Настройки MAX"}}})
	}
	if len(counts) != 0 {
		doc.Components = append(doc.Components, Component{Type: "row", Subtitle: "Статус пересылки", Caption: fmt.Sprintf(
			"Доставлено %d · в очереди %d · пропущено %d · ошибок подготовки %d · результат неизвестен %d",
			counts["sent"], counts["pending"]+counts["sending"], counts["skipped"], counts["failed"], counts["unknown"])})
		if counts["failed"] > 0 {
			doc.Components = append(doc.Components, Component{Type: "text", Style: "secondary",
				Text: "При ошибках подготовки проверьте доверенную локальную модель и подключение мессенджера."})
		}
		if counts["unknown"] > 0 {
			doc.Components = append(doc.Components, Component{Type: "text", Style: "secondary",
				Text: "Доставку с неизвестным результатом можно проверить в чате получателя. Сёма не повторяет такие отправки."})
		}
	}
	return nil
}

func (s *Server) saveWeChatForward(ctx context.Context, req actionRequest) error {
	rule := store.WeChatForwardRule{
		Enabled:    req.Params["enabled"] == "true",
		SourceLang: strings.TrimSpace(req.Params["source_language"]),
		TargetLang: strings.TrimSpace(req.Params["target_language"]),
	}
	languages := s.eng.TranslateLangs()
	if rule.SourceLang != engine.WeChatForwardOriginal && rule.SourceLang != engine.WeChatForwardAny &&
		!hasLanguage(languages, rule.SourceLang) {
		return fmt.Errorf("выберите исходный язык или «Без перевода»")
	}
	if !hasLanguage(languages, rule.TargetLang) && rule.TargetLang != s.eng.TranslateTo() {
		return fmt.Errorf("выберите язык перевода")
	}
	recipients, err := s.eng.Store().WeChatForwardRecipients(ctx)
	if err != nil {
		return err
	}
	chosen := make(map[string]bool)
	for key, value := range req.Params {
		if strings.HasPrefix(key, recipientPrefix) && value == "true" {
			chosen[key] = true
		}
	}
	for _, recipient := range recipients {
		key := recipientField(recipient.Target)
		if chosen[key] {
			rule.Targets = append(rule.Targets, recipient.Target)
			delete(chosen, key)
		}
	}
	if len(chosen) != 0 {
		return fmt.Errorf("права выбранных получателей изменились. Обновите экран")
	}
	return s.eng.WeChatSaveForwardRule(ctx, req.Target, rule)
}
