package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/wechat"
)

const wechatScreen = "settings:wechat"
const wechatQRPath = "/api/wechat/qr"
const wechatFilePath = "/api/wechat/file"

func (s *Server) handleWeChatQR(w http.ResponseWriter, r *http.Request) {
	if !s.mayAdmin(r) {
		http.Error(w, "QR WeChat доступен только хозяину", http.StatusForbidden)
		return
	}
	image, _ := s.eng.WeChatQR()
	if len(image) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(image)
}

func (s *Server) buildWeChat(doc *Document, screen, filter, search string, limit int) error {
	doc.Heading = "WeChat"
	if doc.offline {
		// Браузер не должен подменять экран чата пустой заготовкой.
		doc.partial = true
		return nil
	}
	row := Component{Type: "row", Subtitle: "Аккаунт не подключён"}
	if s.eng.WeChatConnected() {
		row.Subtitle = "Сессия сохранена"
		row.Actions = []Action{
			{Action: "wechat_refresh", Label: "Обновить список и сообщения"},
			{Action: "wechat_disconnect", Label: "Отключить WeChat"},
		}
	} else if image, until := s.eng.WeChatQR(); len(image) > 0 {
		row.Title = "QR для входа в WeChat"
		row.Subtitle = "Отсканируйте код телефоном и подтвердите вход"
		row.Image = fmt.Sprintf("%s?t=%d", wechatQRPath, until.UnixNano())
		row.inlinePNG = image
		row.Expanded = true
		row.Expand = "QR-код"
		row.Actions = []Action{
			{Action: "wechat_poll", Label: "Проверить вход", Style: "primary"},
			{Action: "wechat_begin", Label: "Получить новый QR"},
			{Action: "wechat_disconnect", Label: "Отменить"},
		}
	} else {
		row.Actions = []Action{{Action: "wechat_begin", Label: "Получить QR", Style: "primary"}}
	}
	doc.Components = append(doc.Components, Component{Type: "section", Title: "Вход", Children: []Component{row}})
	if !s.eng.WeChatConnected() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, encoded, ok := strings.Cut(screen, "@"); ok {
		peer, err := url.PathUnescape(encoded)
		if err != nil || peer == "" {
			return fmt.Errorf("неверный адрес чата WeChat")
		}
		chat, err := s.eng.WeChatChat(ctx, peer)
		if errors.Is(err, sql.ErrNoRows) {
			doc.Components = append(doc.Components, emptyState("Чат не найден", "Откройте чат из списка WeChat", ""))
			return nil
		}
		if err != nil {
			return err
		}
		doc.Heading = wechatChatName(chat)
		doc.Components = append(doc.Components, rightsMatrix("wechat-chat-rights", "Права чата", "",
			tgRightColumns, []rightsRow{wechatChatRow(chat)}))
		if chat.Admin {
			doc.Components = append(doc.Components, Component{Type: "text",
				Text: "Команды владельца начинаются с /sema. Примеры: /sema статус, /sema задачи, /sema помощь."})
		}
		doc.Components = append(doc.Components, Component{Type: "button", Actions: []Action{
			{Action: "$goto", Target: wechatScreen, Label: "Все собеседники"},
		}})
		if chat.Collect && !chat.Blocked {
			if err := s.buildWeChatForward(ctx, doc, chat); err != nil {
				return err
			}
		}
		messages, err := s.eng.WeChatText(ctx, peer)
		if err != nil {
			return err
		}
		group := Component{Type: "section", Title: "Сообщения"}
		if chat.Blocked {
			group.Children = []Component{emptyState("Включено «Не трогать»", "Сёма не читает этот чат и не отправляет в него сообщения", "")}
		} else if !chat.Collect {
			group.Children = []Component{emptyState("Подписка выключена", "Включите чтение, чтобы Сёма сохраняла новые сообщения этого чата", "")}
		} else if len(messages) == 0 {
			group.Children = []Component{emptyState("Сохранённых сообщений пока нет", "Здесь появятся сообщения, полученные после подписки", "")}
		}
		for i := len(messages) - 1; i >= 0; i-- {
			group.Children = append(group.Children, Component{Type: "text", Text: messages[i]})
		}
		doc.Components = append(doc.Components, group)
		files, err := s.eng.WeChatFiles(ctx, peer)
		if err != nil {
			return err
		}
		if len(files) != 0 {
			section := Component{Type: "section", Title: "Файлы"}
			for _, file := range files {
				query := url.Values{"peer": {peer}, "id": {file.ID}}
				section.Children = append(section.Children, Component{Type: "row", Subtitle: file.Name,
					Actions: []Action{{Action: "$open", Target: wechatFilePath + "?" + query.Encode(), Label: "Скачать"}}})
			}
			doc.Components = append(doc.Components, section)
		}
		if chat.CanSend {
			doc.Components = append(doc.Components, Component{
				Type: "form", Title: "Сообщение",
				Fields:  []Field{{Name: "text", Type: "text", Label: "Текст"}},
				Actions: []Action{{Action: "wechat_send_text", Target: peer, Label: "Отправить"}},
			})
			doc.Components = append(doc.Components, Component{
				Type: "form", Title: "Отправить файл",
				Fields: []Field{{Name: "file_id", Type: "text", Label: "Номер файла из раздела «Файлы»"}},
				Actions: []Action{
					{Action: "wechat_send_file", Target: peer, Label: "Отправить файл"},
					{Action: "wechat_send_image", Target: peer, Label: "Отправить как JPEG"},
				},
			})
		}
		return nil
	}
	chats, err := s.eng.WeChatChats(ctx)
	if err != nil {
		return err
	}
	doc.Searchable, doc.SearchHint, doc.ownSearch = true, "Имя или идентификатор чата WeChat", true
	buildWeChatList(doc, chats, filter, search, limit)
	return nil
}

func buildWeChatList(doc *Document, chats []store.WeChatChat, filter, search string, limit int) {
	if filter != "subscribed" {
		filter = "all"
	}
	doc.ActiveFilter = filter
	var found []store.WeChatChat
	var subscribed, granted int
	for _, chat := range chats {
		if chat.Collect {
			subscribed++
		}
		if wechatGranted(chat) {
			granted++
		}
		if (filter == "all" || chat.Collect) && looksLike(search, wechatChatName(chat), chat.Peer, wechatChatKind(chat.Peer)) {
			found = append(found, chat)
		}
	}
	doc.Filters = []Filter{
		{ID: "all", Title: fmt.Sprintf("Все · %d", len(chats))},
		{ID: "subscribed", Title: fmt.Sprintf("Подписки · %d", subscribed)},
	}
	if len(found) == 0 {
		title, hint := "Чаты не найдены", "Измените поиск или откройте список всех чатов"
		if len(chats) == 0 {
			title, hint = "Список чатов ещё пуст", "Нажмите «Обновить список и сообщения», чтобы загрузить чаты WeChat"
		} else if filter == "subscribed" && subscribed == 0 {
			title, hint = "Подписок пока нет", "Откройте «Все» и включите «Читать» у нужных чатов"
		}
		doc.Components = append(doc.Components, Component{Type: "section", Title: "Собеседники", Children: []Component{
			emptyState(title, hint, "bubble.left.and.bubble.right"),
		}})
		return
	}
	shown := min(len(found), matrixPage(limit, 40))
	rows := make([]rightsRow, 0, shown)
	for _, chat := range found[:shown] {
		rows = append(rows, wechatChatRow(chat))
	}
	title := fmt.Sprintf("Собеседники · %d, права выданы %d", len(chats), granted)
	if filter != "all" || strings.TrimSpace(search) != "" {
		title = fmt.Sprintf("Собеседники · нашлось %d из %d", len(found), len(chats))
	}
	doc.Components = append(doc.Components, rightsMatrix("wechat-chats", title,
		"«Читать» подписывает на сообщения, «Файлы» включает сбор вложений, «Писать первым» разрешает отправку от вашего имени. "+
			"«Отвечать» разрешает автоответы в личном чате, «Админ» принимает команды /sema только от владельца. "+
			"«Не трогать» снимает права чата, «Напоминать» показывает ожидание ответа.",
		tgRightColumns, rows))
	if shown < len(found) {
		doc.rows = shown
		doc.Components = append(doc.Components, moreRows(shown, len(found)))
	}
}

func wechatGranted(chat store.WeChatChat) bool {
	return chat.Collect || chat.Files || chat.CanSend || chat.Wait || chat.Reply || chat.Admin
}

func wechatChatKind(peer string) string {
	switch {
	case strings.HasSuffix(peer, "@chatroom"):
		return "группа"
	case strings.HasPrefix(peer, "gh_"):
		return "официальный аккаунт"
	case peer == "filehelper":
		return "служебный"
	default:
		return "личный"
	}
}

func wechatChatRow(chat store.WeChatChat) rightsRow {
	name := wechatChatName(chat)
	row := rightsRow{Title: name, Badges: []Badge{{Text: wechatChatKind(chat.Peer), Color: "#8E8E93"}},
		Cells: map[string]Cell{
			"reply": rightNA("Автоответы доступны только в личной переписке с собеседником"),
			"admin": rightNA("Админ доступен только в своём чате и в «Передаче файлов»"),
		}, Menu: []Action{{Action: "$goto", Target: wechatChatScreen(chat.Peer), Label: "Открыть чат"}}}
	var facts []string
	if chat.Title != "" && chat.Title != chat.Peer && wechatChatKind(chat.Peer) == "личный" {
		facts = append(facts, "@"+chat.Peer)
	}
	if chat.Count > 0 {
		facts = append(facts, "собрано "+strconv.Itoa(chat.Count))
	}
	row.Caption = strings.Join(facts, " · ")
	extra := func(right, label string, enabled bool) Cell {
		action := "wechat_enable_" + right
		if enabled {
			action = "wechat_disable_" + right
		}
		return rightCell(name, label, enabled, Action{Action: action, Target: chat.Peer})
	}
	row.Cells["blocked"] = extra("blocked", "не трогать", chat.Blocked)
	if chat.Blocked {
		row.Level = LevelAlert
		for _, right := range []string{"collect", "files", "send", "wait", "reply", "admin"} {
			row.Cells[right] = rightNA("Сначала выключите «Не трогать»")
		}
		return row
	}
	row.Cells["collect"] = rightCell(name, "читать", chat.Collect, wechatCollectAction(chat))
	row.Cells["files"] = extra("files", "забирать вложения при включённом чтении", chat.Files)
	row.Cells["send"] = rightCell(name, "писать первым", chat.CanSend, wechatSendAction(chat))
	row.Cells["wait"] = rightNA("Для напоминаний включите чтение в этом чате")
	if chat.Collect {
		row.Cells["wait"] = extra("wait", "напоминать, если не ответил", chat.Wait)
	}
	if chat.SupportsWeChatReply() {
		row.Cells["reply"] = rightNA("Для автоответов включите чтение")
		if chat.Collect {
			cell := extra(store.WeChatReplyMode, "автоматически отвечать", chat.Reply)
			if !chat.Reply {
				cell.Action.Confirm = "Разрешить Сёме автоматически отвечать в этом чате? Ответы уйдут от вашего имени."
				cell.Action.ConfirmOK = "Разрешить автоответы"
			}
			row.Cells["reply"] = cell
		}
	}
	if chat.SupportsWeChatAdmin() {
		row.Cells["admin"] = rightNA("Для админ-команд включите чтение")
		if chat.Collect {
			cell := extra(store.WeChatAdminMode, "принимать команды владельца /sema", chat.Admin)
			if !chat.Admin {
				cell.Action.Confirm = "Включить команды /sema, отправленные с вашего аккаунта в этом чате? Команды серверу ждут подтверждения в Сёме."
				cell.Action.ConfirmOK = "Включить админ-режим"
			}
			row.Cells["admin"] = cell
		}
	}
	if wechatGranted(chat) {
		row.Level = LevelOK
	}
	return row
}

func wechatChatName(chat store.WeChatChat) string {
	if chat.Peer == "filehelper" {
		return "Передача файлов"
	}
	if chat.Title != "" {
		return chat.Title
	}
	return chat.Peer
}

func wechatCollectAction(chat store.WeChatChat) Action {
	if chat.Collect {
		return Action{Action: "wechat_disable_collect", Target: chat.Peer, Label: "Отписаться"}
	}
	return Action{Action: "wechat_enable_collect", Target: chat.Peer, Label: "Подписаться"}
}

func wechatSendAction(chat store.WeChatChat) Action {
	if chat.CanSend {
		return Action{Action: "wechat_disable_send", Target: chat.Peer, Label: "Запретить отправку"}
	}
	return Action{Action: "wechat_enable_send", Target: chat.Peer, Label: "Разрешить отправку"}
}

func (s *Server) wechatActions(req actionRequest) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	switch req.Action {
	case "wechat_begin":
		return wechatScreen, s.eng.WeChatBegin(ctx)
	case "wechat_poll":
		state, err := s.eng.WeChatPoll(ctx)
		if err != nil {
			return wechatScreen, err
		}
		switch state {
		case wechat.LoginConnected:
			return wechatScreen, nil
		case wechat.LoginLANRequired:
			return wechatScreen, fmt.Errorf("WeChat ждёт подтверждения через локальную сеть. Подключите телефон к той же сети, где работает Сёма, и снова нажмите «Проверить вход»")
		case wechat.LoginScanned:
			return wechatScreen, fmt.Errorf("подтвердите вход на телефоне")
		case wechat.LoginRejected:
			return wechatScreen, fmt.Errorf("WeChat отклонил вход")
		default:
			return wechatScreen, fmt.Errorf("WeChat ждёт сканирования QR")
		}
	case "wechat_disconnect":
		return wechatScreen, s.eng.WeChatDisconnect()
	case "wechat_refresh":
		return wechatScreen, s.eng.WeChatRefresh(ctx)
	case "wechat_save_forward":
		return wechatChatScreen(req.Target), s.saveWeChatForward(ctx, req)
	case "wechat_enable_send", "wechat_disable_send":
		return wechatSettingsAfterAction(req), s.eng.WeChatSetSend(ctx, req.Target, req.Action == "wechat_enable_send")
	case "wechat_send_file", "wechat_send_image":
		id, err := strconv.ParseInt(strings.TrimSpace(req.Params["file_id"]), 10, 64)
		if err != nil || id <= 0 {
			return wechatScreen, fmt.Errorf("укажите номер файла Сёмы")
		}
		return wechatScreen + "@" + url.PathEscape(req.Target), s.eng.WeChatSendFile(ctx, req.Target, id, req.Action == "wechat_send_image")
	case "wechat_send_text":
		return wechatScreen + "@" + url.PathEscape(req.Target), s.eng.WeChatSendText(ctx, req.Target, req.Params["text"])
	case "wechat_enable_collect", "wechat_disable_collect":
		return wechatSettingsAfterAction(req), s.eng.WeChatSetCollect(ctx, req.Target, req.Action == "wechat_enable_collect")
	case "wechat_enable_files", "wechat_disable_files", "wechat_enable_blocked", "wechat_disable_blocked", "wechat_enable_wait", "wechat_disable_wait",
		"wechat_enable_reply", "wechat_disable_reply", "wechat_enable_admin", "wechat_disable_admin":
		enabled := strings.HasPrefix(req.Action, "wechat_enable_")
		right := strings.TrimPrefix(strings.TrimPrefix(req.Action, "wechat_enable_"), "wechat_disable_")
		return wechatSettingsAfterAction(req), s.eng.WeChatSetChatRight(ctx, req.Target, right, enabled)
	}
	return "", errUnhandled
}

func wechatSettingsAfterAction(req actionRequest) string {
	if req.Screen == wechatScreen+"@"+url.PathEscape(req.Target) {
		return req.Screen
	}
	return wechatScreen
}

func (s *Server) handleWeChatFile(w http.ResponseWriter, r *http.Request) {
	if !s.mayAdmin(r) {
		http.Error(w, "файл WeChat доступен только хозяину", http.StatusForbidden)
		return
	}
	peer, id := r.URL.Query().Get("peer"), r.URL.Query().Get("id")
	if peer == "" || len(peer) > 512 || id == "" || len(id) > 128 {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	info, data, err := s.eng.WeChatDownload(ctx, peer, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Не удалось получить файл WeChat", http.StatusBadGateway)
		return
	}
	defer clear(data)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name}))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}
