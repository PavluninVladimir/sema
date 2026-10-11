package api

import (
	"fmt"
	"strconv"
	"strings"
)

// Действия чатов и файлов.
// Часть общего разбора действий: execute перебирает такие группы, пока
// одна из них не возьмёт действие себе; чужое действие — errUnhandled.

func (s *Server) chatsActions(req actionRequest) (string, error) {
	one := func() (int64, error) { return req.one() }
	switch req.Action {
	case "new_chat":
		id, err := s.eng.Store().CreateChat("Новый чат")
		if err != nil {
			return "", err
		}
		return "chat:" + strconv.FormatInt(id, 10), nil
	case "send_message":
		id, err := one()
		if err != nil {
			return "", err
		}
		// вопрос Сёме из ленты собеседника уходит в тред «Сёма»: экран
		// открывает тред, чтобы было видно, куда ушло написанное
		landed, err := s.eng.SendMessageTo(id, req.Params["text"])
		if err != nil {
			return "", err
		}
		if landed != id {
			return "chat:" + strconv.FormatInt(landed, 10), nil
		}
		return "", nil
	case "thread_from_msg":
		msgID, err := one()
		if err != nil {
			return "", err
		}
		id, err := s.eng.ThreadFromMsg(msgID)
		if err != nil {
			return "", err
		}
		return "chat:" + strconv.FormatInt(id, 10), nil
	case "set_chat_model":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		return "", s.eng.Store().SetChatModel(id, modelName(req.Params["value"]))
	case "set_chat_effort":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		return "", s.eng.Store().SetChatEffort(id, req.Params["value"])
	case "rename_chat":
		id, err := one()
		if err != nil {
			return "", err
		}
		title := strings.TrimSpace(req.Params["value"])
		if title == "" {
			return "", nil
		}
		return "", s.eng.Store().RenameChat(id, title)
	case "stop_chat":
		id, err := one()
		if err != nil {
			return "", err
		}
		s.eng.StopChat(id)
		return "", nil
	case "delete_chat":
		id, err := one()
		if err != nil {
			return "", err
		}
		// переписка с человеком не удаляется из чата: собеседника закрывают
		// в настройках, а разговор остаётся в памяти
		if peer, ok := s.eng.TGPeerOfChat(id); ok {
			return "", fmt.Errorf("переписку с «%s» удалить нельзя: закройте чат в «Настройки → Telegram», история останется в памяти", peer.Title)
		}
		if peer, ok := s.eng.MAXPeerOfChat(id); ok {
			return "", fmt.Errorf("переписку с «%s» удалить нельзя: закройте чат в «Настройки → MAX», история останется в памяти", peer.Title)
		}
		return "ask", s.eng.DeleteChatTree(id)
	case "delete_file":
		id, err := one()
		if err != nil {
			return "", err
		}
		return "", s.eng.DeleteAttachment(id)
	default:
		return "", errUnhandled
	}
}
