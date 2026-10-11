package api

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/htmlsafe"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// ---- Чаты ----

// buildChats — разговор на широком экране. Списка рядом больше нет: дерево
// разговоров живёт в главном меню (chatScreens), а вторая панель с тем же
// деревом дублировала его и отнимала ширину у самого разговора.
func (s *Server) buildChats(doc *Document, chatID int64, limit int) error {
	chats, err := s.eng.Store().Chats()
	if err != nil {
		return err
	}
	// без выбранного — разговор с Сёмой: сюда приходит всё, что ждёт
	// решения, и с него человек начинает. Нет такого — самый свежий
	// разговор; у контейнера ленты нет, открывать его незачем
	if chatID == 0 {
		chatID = mainConversation(chats)
	}
	if chatID == 0 {
		for _, c := range chats {
			if !c.IsContainer() {
				chatID = c.ID
				break
			}
		}
	}
	if chatID == 0 {
		doc.Heading = "Чаты с Сёмой"
		doc.Subheading = "Сёма помнит контекст чата и видит ваши письма, файлы и чеки"
		doc.ToolbarActions = []Action{{Action: "new_chat", Label: "Новый чат", Style: "primary"}}
		doc.Components = append(doc.Components, emptyState("Здесь будут разговоры",
			"Свои чаты, переписка из мессенджеров и ветки «Сёма» под ними. Начните с «Нового чата»: спросите про письма, файлы или серверы — Сёма помнит разговор и видит ваш архив.",
			"bubble.left.and.bubble.right"))
		return nil
	}
	// какой разговор открыт — видно в меню: адрес «ask» сам по себе не
	// говорит, где человек стоит
	doc.Active = "chat:" + strconv.FormatInt(chatID, 10)
	s.markChatRead(chatID)
	return s.buildChat(doc, chatID, limit)
}

// buildContainer — группа с топиками: своей ленты нет, экран
// перечисляет разговоры под ней. Писать сюда некуда — пишут в топик.
func (s *Server) buildContainer(doc *Document, chat *store.Chat) error {
	st := s.eng.Store()
	title := strings.TrimPrefix(strings.TrimPrefix(chat.Title, "Telegram · "), "MAX · ")
	doc.Heading = title
	doc.Layout = "read"
	kind, one, few, many := "группа с топиками", "топик", "топика", "топиков"
	kids, _ := st.ChatChildren(chat.ID)
	var convs []*store.Chat
	for _, k := range kids {
		if !k.IsThread() {
			convs = append(convs, k)
		}
	}
	doc.Subheading = kind + " · " + plural(len(convs), one, few, many) + " · у каждого своя лента и своя ветка с Сёмой"
	if peer, ok := s.eng.TGPeerOfChat(chat.ID); ok {
		doc.ToolbarActions = []Action{{Action: "$goto", Target: "settings:telegram", Label: "Настройки «" + peer.Title + "»"}}
		// темы, о которых Telegram сказал, а сообщений ещё не было
		var silent []string
		for _, t := range s.eng.TGTopicsOf(peer.Account, peer.ID) {
			if _, ok := st.ConversationByTopic(chat.ID, int64(t.ID)); !ok {
				silent = append(silent, t.Title)
			}
		}
		if len(silent) > 0 {
			doc.Footer = "Ещё без сообщений: " + strings.Join(clipList(silent, 8), ", ")
		}
	}
	if len(convs) == 0 {
		doc.Components = append(doc.Components, emptyState("Разговоров пока нет",
			"Разговор по теме появится с первым сообщением в ней. Права на темы — в настройках мессенджера.",
			"bubble.left.and.bubble.right"))
		return nil
	}
	// строки тем — той же формой, что и список чатов: имя жирным, под ним
	// последняя реплика, время справа. Раньше имя темы шло мелкой серой
	// надстрокой над превью, и старшинство читалось наоборот
	var строки []Component
	for _, c := range convs {
		превью := chatPreview(c.LastMsg, "Telegram")
		if превью == "" {
			превью = "пока пусто"
		}
		row := Component{Type: "row", Layout: "wide", ID: "conv-" + strconv.FormatInt(c.ID, 10),
			Icon: "bubble.left", Subtitle: c.Title, Caption: превью,
			Meta: whenShort(c.LastAt), MetaInline: true,
			Actions: []Action{{Action: "$goto", Target: "chat:" + strconv.FormatInt(c.ID, 10), Label: "Открыть"}}}
		if s.chatUnread(c) {
			withLevel(&row, LevelInfo)
			row.Dot = levelDot(LevelInfo)
		}
		строки = append(строки, row)
	}
	doc.Components = append(doc.Components, Component{Type: "section", Children: строки})
	return nil
}

// clipList — не больше n имён, дальше счётчик.
func clipList(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	out := append([]string{}, list[:n]...)
	return append(out, fmt.Sprintf("и ещё %d", len(list)-n))
}

func (s *Server) buildChat(doc *Document, chatID int64, limit int) error {
	st := s.eng.Store()
	chat, err := st.Chat(chatID)
	if err != nil {
		return err
	}
	if chat.IsContainer() {
		return s.buildContainer(doc, chat)
	}
	// переписка отдаётся порциями: раньше было ровно 50 последних, и
	// более ранних в интерфейсе не существовало вовсе
	page := s.chatPage()
	if limit > page {
		page = limit
	}
	msgs, err := st.ChatMsgs(chatID, page+1)
	if err != nil {
		return err
	}
	more := len(msgs) > page
	if more {
		msgs = msgs[len(msgs)-page:]
	}
	id := strconv.FormatInt(chatID, 10)
	model, effort := s.eng.ChatSettings(chatID)
	doc.Heading = chat.Title
	// колонка шире, чем у письма: панели со списком рядом больше нет, и
	// узкая лента посреди пустого окна читалась как недостроенный экран
	doc.Layout = "wide"
	// подпись чата — фактами: модель, усилие и кому уходит написанное.
	// Одна и та же строка у всех чатов ничего не говорила
	facts := []string{"модель " + model, effort}
	tgPeer, isTG := s.eng.TGPeerOfChat(chatID)
	maxPeer, isMAX := s.eng.MAXPeerOfChat(chatID)
	зов := "«" + s.eng.TGHelpWord() + ", …» в начале строки позовёт Сёму"
	switch {
	case s.isMainConversation(chat.ID):
		// главный разговор: длинное объяснение про ветку человеку здесь
		// не нужно — он в нём живёт, а не разбирается, как он устроен
		facts = append([]string{"ваш разговор с Сёмой"}, facts...)
	case chat.IsThread():
		// ветка с Сёмой: собеседник её не видит, а Сёма видит родителя
		facts = append([]string{s.eng.ThreadTitle(chat),
			"сюда идут ответы Сёмы, карточки и черновики — собеседник этого не видит"}, facts...)
	case isTG && tgPeer.Kind == "channel":
		facts = append([]string{"канал «" + tgPeer.Title + "» одной лентой, как в Telegram",
			"написанное здесь уходит в канал постом, без Сёмы", зов}, facts...)
	case isTG && tgPeer.Topic > 0:
		facts = append([]string{"тема «" + tgPeer.TopicTitle + "» группы «" + tgPeer.Title + "»",
			"написанное уходит в тему сразу, без Сёмы", зов}, facts...)
	case isTG:
		facts = append([]string{"переписка с «" + tgPeer.Title + "»",
			"написанное уходит ему сразу, без Сёмы", зов}, facts...)
	case isMAX:
		facts = append([]string{"переписка с «" + maxPeer.Title + "» в MAX",
			"написанное уходит ему сразу, без Сёмы", зов}, facts...)
	default:
		facts = append([]string{"Сёма помнит этот разговор и видит ваши письма, файлы и чеки"}, facts...)
	}
	// счёт ждущих — первым и словами: он главное в этой строке, а в меню
	// его ставить нельзя — цифру рядом с чатом читают как непрочитанное
	if s.isMainConversation(chat.ID) {
		if n := s.eng.NotifyCounter(); n > 0 {
			facts = append([]string{plural(n, "ждёт решения", "ждут решения", "ждут решения")}, facts...)
		}
	}
	doc.Subheading = strings.Join(facts, " · ")
	// модель и уровень размышления — у каждого чата свои
	doc.HeaderSelects = []Select{
		// провайдер рядом с именем: «gpt-5.6-luna» бывает и у OpenAI, и у
		// чужого шлюза, а по имени этого не видно
		{Name: "value", Value: withProvider(s, model), Options: modelsWithProviderList(s),
			Action: Action{Action: "set_chat_model", Target: id}},
		{Name: "value", Value: effort, Options: engine.EffortLevels(),
			Action: Action{Action: "set_chat_effort", Target: id}},
	}
	// «Новый чат» жил кнопкой над панелью со списком; панели нет — кнопка в шапке
	doc.ToolbarActions = append(doc.ToolbarActions, Action{Action: "new_chat", Label: "Новый чат"})
	doc.ToolbarMenu = []Action{
		{Action: "rename_chat", Target: id, Label: "Переименовать", Prompt: "Название чата", Value: chat.Title},
		{Action: "delete_chat", Target: id, Label: "Удалить", Style: "destructive", ConfirmOK: "Удалить",
			Confirm: "Удалить чат «" + chat.Title + "»? Переписку не восстановить."},
	}
	if s.isMainConversation(chat.ID) {
		// пункта «Ждут вас» в меню больше нет — записи приходят сюда;
		// но разобранное и отложенное должно остаться достижимым
		doc.ToolbarMenu = append([]Action{
			{Action: "$goto", Target: notifyScreen, Label: "Все записи"},
		}, doc.ToolbarMenu...)
	}
	if chat.IsThread() {
		doc.ToolbarActions = append([]Action{{Action: "$goto", Target: s.eng.ParentChat(chat), Label: "К разговору"}}, doc.ToolbarActions...)
	}
	// переписку с человеком не удаляют мимоходом: телеграм-чат закрывается
	// в настройках собеседника, а история остаётся в памяти
	if isTG {
		// решение «отвечать вместо меня» принимается при чтении переписки,
		// а жило только в настройках канала
		reply := "Отвечать вместо меня: выключено"
		if tgPeer.Reply {
			reply = "Отвечать вместо меня: включено"
		}
		doc.ToolbarActions = []Action{
			{Action: "tg_chat_right", Target: tgTarget(tgPeer.Account, tgPeer.ID) + "~reply", Label: reply},
		}
		doc.ToolbarMenu = []Action{
			{Action: "$goto", Target: "settings:telegram", Label: "Настройки «" + tgPeer.Title + "»"},
		}
		if tgPeer.Topic > 0 {
			// у топика право своё: переключатель здесь ставит его этой теме,
			// а не всей группе
			doc.ToolbarActions = []Action{
				{Action: "$goto", Target: "chat:" + strconv.FormatInt(chat.Parent, 10), Label: "К группе"},
			}
			doc.ToolbarMenu = []Action{
				{Action: "tg_topic_right", Target: fmt.Sprintf("%s/%d~reply", tgTarget(tgPeer.Account, tgPeer.ID), tgPeer.Topic), Label: reply + " в этом топике"},
				{Action: "$goto", Target: "settings:telegram", Label: "Настройки «" + tgPeer.Title + "»"},
			}
		}
	}
	if isMAX {
		doc.ToolbarActions = nil
		doc.ToolbarMenu = []Action{
			{Action: "$goto", Target: "settings:max", Label: "Настройки «" + maxPeer.Title + "»"},
		}
	}
	// в телеграм-чате написанное уходит собеседнику, а не модели: подпись
	// поля говорит об этом прямо, иначе разговор уйдёт не туда
	placeholder := "Написать Сёме…"
	if peer, ok := s.eng.TGPeerOfChat(chatID); ok {
		placeholder = "Написать «" + peer.Title + "» · «" + s.eng.TGHelpWord() + ", …» позовёт Сёму"
		switch {
		case peer.Kind == "channel":
			// в ленте канала написанное — это пост в канал, а не ответ
			// собеседнику: без этой подписи легко опубликовать реплику
			placeholder = "Новый пост в «" + peer.Title + "» · «" + s.eng.TGHelpWord() + ", …» позовёт Сёму"
		case peer.Topic > 0:
			placeholder = "Написать в «" + peer.TopicTitle + "» · «" + s.eng.TGHelpWord() + ", …» позовёт Сёму"
		}
	}
	if chat.IsThread() {
		placeholder = "Написать Сёме · собеседник этого не видит"
	}
	doc.Composer = &Composer{
		Placeholder: placeholder,
		Field:       "text",
		Submit:      Action{Action: "send_message", Target: id, Label: "Отправить", Style: "primary", Deferrable: true},
		Attach:      &Action{Action: "attach_file", Target: id, Label: "Прикрепить файл", Deferrable: true},
	}
	if inChannelChat := isTG || isMAX; inChannelChat {
		who := ""
		if isTG {
			who = tgPeer.Title
			if tgPeer.Topic > 0 {
				who = tgPeer.TopicTitle
			}
		} else {
			who = maxPeer.Title
		}
		doc.Composer.Peer = who
		doc.Composer.Call = s.eng.TGHelpWord()
	}
	if more {
		doc.Components = append(doc.Components, Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: "Показать более ранние"}}})
	}
	// у разговора с собеседником есть тред «Сёма»: всё, что Сёма про него
	// сказал и сделал, лежит там, а не в ленте. Строка ведёт в тред
	if (isTG || isMAX) && !chat.IsThread() {
		for _, k := range s.chatThreads(chatID) {
			doc.Components = append(doc.Components, Component{Type: "row", ID: "thread-" + strconv.FormatInt(k.ID, 10),
				Icon: "bubble.left.and.text.bubble.right", Title: k.Title,
				Caption: chatPreview(k.LastMsg, "Мои чаты") + " · " + whenShort(k.LastAt),
				Actions: []Action{{Action: "$goto", Target: "chat:" + strconv.FormatInt(k.ID, 10), Label: "Открыть"}}})
		}
	}
	// в чате собеседника разговор с Сёмой идёт веткой: два потока в одной
	// ленте иначе не различить, а живую переписку рвать нельзя
	inChannel := isTG || isMAX
	var thread []Component
	threadID := ""
	closeThread := func() {
		if len(thread) == 0 {
			return
		}
		sec := Component{Type: "section", Style: "thread", ID: threadID,
			Title: "Сёма · " + plural(len(thread), "реплика", "реплики", "реплик"),
			// свёрнутость решается один раз, при сборке экрана: документ
			// обновляется каждые пять секунд, и лента не должна прыгать
			Collapsed: !threadWaits(thread),
			Children:  thread,
		}
		doc.Components = append(doc.Components, sec)
		thread, threadID = nil, ""
	}
	add := func(c Component, intoThread bool) {
		if inChannel && intoThread {
			if threadID == "" {
				threadID = c.ID
			}
			thread = append(thread, c)
			return
		}
		closeThread()
		doc.Components = append(doc.Components, c)
	}
	// команды, чьи карточки уже стоят в ленте: внизу их повторять незачем
	показаны := map[int64]bool{}
	показаныMCP := map[int64]bool{}
	показаныБезПравила := map[int64]bool{}
	day := ""
	for _, m := range msgs {
		// разделитель дня: переписка за три дня выглядела как один
		if d := dayLine(m.At); d != day {
			day = d
			closeThread()
			// день разговора — по центру ленты, а не подписью слева
			doc.Components = append(doc.Components,
				Component{Type: "text", Style: "day", Text: d})
		}
		if m.Role == "tool" {
			if card, ok := engine.ParseToolCard(m.Content); ok {
				if card.SSH != nil && card.SSH.PendingID > 0 {
					показаны[card.SSH.PendingID] = true
				}
				if card.MCP != nil && card.MCP.PendingID > 0 {
					показаныMCP[card.MCP.PendingID] = true
				}
				if card.SSH != nil && card.SSH.NoRuleID > 0 {
					показаныБезПравила[card.SSH.NoRuleID] = true
				}
				// id по номеру сообщения: раскрытие карточки помнится клиентом между обновлениями
				c := s.toolCardComponent(card, m.At.Format("15:04"), m.ID)
				if c.ID == "" {
					c.ID = fmt.Sprintf("card-%d", m.ID)
				}
				add(c, true) // карточки — часть разговора с Сёмой
				continue
			}
		}
		// кто написал — над пузырём, время — под ним. Одной строкой снизу
		// «Сёма · DeepseekPro · 17:52» читалось как часть реплики, а
		// спрашивают о ленте всегда одно: кто это сказал
		час := m.At.Format("15:04")
		кто, meta := "", час
		switch m.Role {
		case "assistant":
			кто = "Сёма · " + model
		case "sent":
			// это ушло собеседнику от вашего имени, но написал Сёма:
			// пузырь справа, но пунктирный — рука не ваша
			кто, meta = "Сёма от вашего имени", "отправлено · "+час
		case "user":
			meta = "вы · " + час
		case "peer":
			// реплика собеседника: подписываем именем, иначе непонятно,
			// кто говорит — Сёма или человек с той стороны. В группе и
			// топике пишут многие, там имя автора приходит с сообщением;
			// не пришло — подписи нет, выдумывать автора нельзя
			switch {
			case m.Author != "":
				кто = m.Author
			case chat.Kind == store.ChatKindTopic || chat.Kind == store.ChatKindPost:
			default:
				кто = strings.TrimPrefix(strings.TrimPrefix(chat.Title, "Telegram · "), "MAX · ")
			}
		case "note":
			// служебное — не разговор, а отметка на полях: тихая полоска
			// по центру ленты. Пузырём оно спорило с репликами человека
			add(Component{Type: "text", Style: "day",
				ID:   "cm-" + strconv.FormatInt(m.ID, 10),
				Text: "служебное · " + clipRunes(strings.Join(strings.Fields(m.Content), " "), 220)}, false)
			continue
		}
		bubble := Component{
			Type:  "bubble",
			ID:    "cm-" + strconv.FormatInt(m.ID, 10),
			Role:  m.Role,
			Title: кто,
			Text:  m.Content,
			Meta:  meta,
		}
		// от любого сообщения — ветка с Сёмой; в самой ветке ветку не заводят
		if !chat.IsThread() && m.Role != "note" {
			bubble.Menu = []Action{{Action: "thread_from_msg", Target: strconv.FormatInt(m.ID, 10), Label: "Обсудить с Сёмой"}}
		}
		// файл, присланный в чат: чип открывает его через сервер
		if m.Role != "tool" {
			for _, ref := range reChatFileRef.FindAllStringSubmatch(m.Content, 3) {
				if id, err := strconv.ParseInt(ref[1], 10, 64); err == nil {
					if a, err := st.AttachmentByID(id); err == nil {
						bubble.Files = append(bubble.Files, File{ID: a.ID, Name: a.Filename,
							Meta: humanBytes(a.Size), Image: strings.HasPrefix(a.MIME, "image/")})
						if m.Role == "peer" {
							// у реплики из мессенджера «имя (#номер)» — служебная
							// пометка для Сёмы: картинка уже под текстом, а
							// «photo-5287….jpg (#437)» в конце поста — мусор
							bubble.Text = strings.TrimSpace(strings.ReplaceAll(bubble.Text, "«"+a.Filename+"» "+ref[0], ""))
						}
					}
				}
			}
		}
		// обращение к Сёме и её ответ — ветка; реплики собеседника и то,
		// что ушло ему, идут общим потоком
		toThread := m.Role == "assistant" || (m.Role == "user" && s.eng.AsksSema(m.Content))
		add(bubble, toThread)
	}
	closeThread()
	// то, что ждёт человека, видно над полем ввода: раздел внизу ленты
	// уезжает вверх с каждым новым сообщением
	if waiting := s.chatWaiting(chatID); waiting != "" {
		doc.Notice = &Notice{Icon: "hand.raised", Text: waiting}
	}
	// команды этого чата, ждущие решения: всегда внизу, даже если карточка
	// осталась выше окна истории
	if list, err := st.SSHPendingList(); err == nil {
		var rows []Component
		for _, p := range list {
			if p.ChatID != chatID || показаны[p.ID] {
				continue
			}
			id := strconv.FormatInt(p.ID, 10)
			c := Component{Type: "toolcard", ID: "wait-" + id, Icon: "terminal",
				Title: "Команда на «" + p.Host + "»", Meta: "ждёт с " + whenText(p.Created),
				Detail:  p.Cmd,
				Caption: "До нажатия на сервер ничего не уходит.",
				Badges:  []Badge{{Text: "ждёт вас", Level: LevelWarn}},
				Actions: []Action{
					{Action: "approve_ssh", Target: id, Label: "Выполнить", Style: "primary"},
					{Action: "reject_ssh", Target: id, Label: "Отклонить", Style: "destructive"},
				}}
			withLevel(&c, LevelWarn)
			rows = append(rows, c)
		}
		if len(rows) > 0 {
			doc.Components = append(doc.Components, Component{Type: "section",
				Title: "Не поедет без вас · " + strconv.Itoa(len(rows)), Children: rows})
		}
	}
	// команды без правила: запрещены по умолчанию и ждут, заведёт ли человек правило
	var noRule []Component
	for _, n := range st.SSHNoRuleList(50) {
		if n.ChatID != chatID || показаныБезПравила[n.ID] {
			continue
		}
		id := strconv.FormatInt(n.ID, 10)
		c := Component{Type: "toolcard", ID: "wait-rule-" + id, Icon: "terminal",
			Title: "Нет правила для команды", Meta: "ждёт с " + whenText(n.Created),
			Detail:  n.Cmd,
			Caption: "На сервере «" + n.Host + "» нет правила для этой команды — она не выполнена. " + n.Why,
			Badges:  []Badge{{Text: "нет правила", Level: LevelWarn}},
			Actions: []Action{{Action: "rule_ask", Target: id, Label: "Спрашивать для неё", Style: "primary"}}}
		if n.CanAllow {
			c.Actions = append(c.Actions, Action{Action: "rule_allow", Target: id, Label: "Разрешить её"})
		}
		c.Actions = append(c.Actions, Action{Action: "rule_dismiss", Target: id, Label: "Оставить запрещённой", Style: "destructive"})
		withLevel(&c, LevelWarn)
		noRule = append(noRule, c)
	}
	if len(noRule) > 0 {
		doc.Components = append(doc.Components, Component{Type: "section",
			Title: "Не поедет без вас · нет правила · " + strconv.Itoa(len(noRule)), Children: noRule})
	}
	// вызовы чужих серверов, ждущие кнопки, — так же внизу
	if list, err := st.MCPPendingList(); err == nil {
		var rows []Component
		for _, p := range list {
			if p.ChatID != chatID || показаныMCP[p.ID] {
				continue
			}
			id := strconv.FormatInt(p.ID, 10)
			c := Component{Type: "toolcard", ID: "wait-mcp-" + id, Icon: "puzzlepiece.extension",
				Title: "Вызов «" + p.Server + "»", Meta: "ждёт с " + whenText(p.Created),
				Detail:  p.Tool + " " + clipRunes(p.Args, 400),
				Caption: "До нажатия на чужой сервер ничего не уходит.",
				Badges:  []Badge{{Text: "ждёт вас", Level: LevelWarn}},
				Actions: []Action{
					{Action: "approve_mcp", Target: id, Label: "Выполнить", Style: "primary"},
					{Action: "reject_mcp", Target: id, Label: "Отклонить", Style: "destructive"},
					{Action: "approve_mcp_always", Target: id, Label: "Выполнить и разрешить впредь"},
				}}
			withLevel(&c, LevelWarn)
			rows = append(rows, c)
		}
		if len(rows) > 0 {
			doc.Components = append(doc.Components, Component{Type: "section",
				Title: "Не поедет без вас · " + strconv.Itoa(len(rows)), Children: rows})
		}
	}
	// то, что ждёт решения, приходит карточкой сюда, а не в отдельный
	// список: два места для одного и того же человек всё равно читает
	// как одно, а отвечает он здесь
	s.waitingInFeed(doc, chat)
	if thinking, partial := s.eng.Thinking(chatID); thinking {
		// написанное до этой секунды остаётся видно: локальная модель
		// пишет минутами, и пустой экран всё это время — не ответ
		if strings.TrimSpace(partial) != "" {
			doc.Components = append(doc.Components, Component{
				Type: "bubble", ID: "thinking", Role: "assistant",
				Title: "Сёма · " + model, Text: partial, Meta: "пишет прямо сейчас",
			})
		}
		// ожидание именное: кто думает, какой моделью и сколько. «Загрузка»
		// без имени ничего не говорит, а ждать приходится по три минуты
		want := model
		if len(msgs) > 0 {
			if с := waitWith(msgs[len(msgs)-1].At); с != "" {
				want += " · " + с
			}
		}
		doc.Components = append(doc.Components, Component{Type: "thinking", ID: "думает",
			Title: "Сёма думает", Meta: want,
			// ответ можно оборвать: написанное до этого момента остаётся
			Actions: []Action{{Action: "stop_chat", Target: id, Label: "Отменить"}}})
	}
	return nil
}

// waitWith — сколько ждём ответа. Точного начала работы движок не хранит,
// поэтому считаем от последней реплики: вопрос задан — с него и ждём.
// Больше шести часов — молчим, там уже не ожидание, а забытый чат.
func waitWith(at time.Time) string {
	d := time.Since(at)
	switch {
	case d < 0 || d > 6*time.Hour:
		return ""
	case d < time.Minute:
		return fmt.Sprintf("ждём %d с", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("ждём %d мин", int(d.Minutes()))
	}
	return fmt.Sprintf("ждём %d ч", int(d.Hours()))
}

// ---- Задачи ----

// listLimit — сколько строк длинного списка показать: первая порция — из
// настройки ui.list_rows, дальше «Показать ещё» удваивает предел экрана.
func (s *Server) listLimit(limit int) int {
	if limit <= 0 || limit == defaultRows {
		return s.eng.ListRows()
	}
	return limit
}

// listLimitFor — то же и запомнить показанный предел в документе: его клиент
// удвоит по «Показать ещё».
func (s *Server) listLimitFor(doc *Document, limit int) int {
	n := s.listLimit(limit)
	doc.rows = n
	return n
}

func (s *Server) buildTasks(doc *Document, limit int) error {
	tasks, err := s.eng.Store().Tasks()
	if err != nil {
		return err
	}
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	doc.Heading = "Задачи"
	doc.Subheading = "по расписанию и друг за другом; сердцебиение не выключается"
	doc.ToolbarActions = []Action{
		{Action: "reset_tasks", Label: "Вернуть задачи по умолчанию",
			Confirm:   "Вернуть встроенные задачи с их обычным расписанием? Ваши задачи не тронутся.",
			ConfirmOK: "Вернуть"},
		{Action: "new_task_form", Label: "Новая задача", Style: "primary"},
	}
	doc.Subheading = "по расписанию и друг за другом; сердцебиение не выключается"

	byID := map[int64]*store.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}

	if editID := s.editingTask(); editID != 0 {
		for _, t := range tasks {
			if t.ID == editID && !t.Root {
				when := t.Schedule
				if t.AfterID != 0 {
					if dep := byID[t.AfterID]; dep != nil {
						when = "после «" + dep.Title + "»"
					}
				}
				doc.Wizard = s.taskWizard(tasks, t.Title, t.Prompt, when,
					Action{Action: "save_task", Target: strconv.FormatInt(t.ID, 10),
						Label: "Сохранить", Style: "primary"}, "Изменить задачу")
			}
		}
	}
	if s.newTaskFormOpen() {
		names := []string{"— по времени, без очереди"}
		for _, t := range tasks {
			if !t.Root {
				names = append(names, t.Title)
			}
		}
		doc.Wizard = &Wizard{
			Title: "Новая задача",
			Steps: []WizardStep{
				{
					Title: "Что делать",
					Hint:  "Опишите словами — Сёма выполнит это и пришлёт результат в чат задачи",
					Fields: []Field{
						{Type: "text", Name: "what", Label: "Что сделать",
							Hint: "Например: «Собери новости и важное из писем за сутки, коротко по пунктам»"},
					},
				},
				{
					Title: "Когда",
					Hint:  "Выберите готовое расписание или напишите своё",
					Fields: []Field{
						{Type: "chips", Name: "preset", Label: "Как запускать",
							Options: []string{"9:00", "каждый час", "каждые 30 мин", "каждые 5 мин", "каждое вс 20:00"}},
						{Type: "text", Name: "when", Label: "Точное расписание", Value: "9:00",
							Hint: "Своими словами: «каждые 15 мин», «каждое вс 20:00», «30.09 10:00»"},
						{Type: "picker", Name: "after", Label: "Запускать после задачи",
							Value: names[0], Options: names,
							Hint: "Задача стартует сразу после того, как выбранная закончит проход"},
					},
				},
				{
					Title:  "Проверка",
					Hint:   "Проверьте и создайте задачу",
					Fields: []Field{},
				},
			},
			Summary: "«{what}» · {when} · результат придёт в отдельный чат задачи",
			Submit:  Action{Action: "create_task", Label: "Создать", Style: "primary"},
			Cancel:  Action{Action: "new_task_form", Label: "Отмена"},
		}
	}

	var rootRows, rows []Component
	// расход и бюджеты — один раз на весь список: раньше каждая строка с
	// последним запуском стоила трёх запросов к журналу расхода, и экран
	// собирался секунду вместо сотой доли
	usage := s.buildUsage()
	for _, t := range tasks {
		строка, корень := s.taskRow(t, byID, usage)
		if корень {
			rootRows = append(rootRows, строка)
			continue
		}
		rows = append(rows, строка)
	}

	// показатели сверху: состояние всех задач разом, без чтения строк
	working, off, fails := 0, 0, 0
	for _, t := range tasks {
		if t.Root {
			continue
		}
		if t.Enabled {
			working++
		} else {
			off++
		}
		// «отказ» видно по последнему итогу: отдельного счётчика у задачи
		// нет, а врать числом нельзя
		if strings.Contains(strings.ToLower(t.LastResult), "ошибка") {
			fails++
		}
	}
	add(Component{Type: "cards", Children: []Component{
		{Type: "metric", Title: "Работают", Text: strconv.Itoa(working), Caption: "по расписанию"},
		{Type: "metric", Title: "Выключены", Text: strconv.Itoa(off), Caption: "только по кнопке"},
		{Type: "metric", Title: "Отказы", Text: strconv.Itoa(fails), Caption: "за последние проходы"},
		{Type: "metric", Title: "Всего", Text: strconv.Itoa(len(tasks)), Caption: "вместе с корневой"},
	}})

	// поручения — работа «до готовности»: что идёт и что ждёт человека
	if sec, ok := s.assignSection(); ok {
		add(sec)
	}
	if len(rootRows) > 0 {
		add(Component{Type: "section", Title: "Корень", Children: rootRows})
	}
	sys, mine := splitRows(tasks, rows)
	// выключенные — отдельно: иначе они теряются среди работающих, и не
	// видно, что работа не делается
	var on, disabled []Component
	for _, r := range sys {
		if r.Toggle == "off" {
			disabled = append(disabled, r)
		} else {
			on = append(on, r)
		}
	}
	add(Component{Type: "section", Title: "Задачи Сёмы · можно менять", Children: on})
	if len(disabled) > 0 {
		// сами строки — при раскрытии: выключенные смотрят реже всего, а
		// с двадцатью пятью работающими экран не укладывался в бюджет
		add(Component{Type: "section", ID: "tasks-off",
			Title:     fmt.Sprintf("Выключены · %d — работают только по кнопке", len(disabled)),
			Collapsed: true, Lazy: "tasks-off"})
	}
	if len(mine) > 0 {
		// своих задач становится много — из чатов, из скриптов: порциями
		limit = s.listLimitFor(doc, limit)
		всего := len(mine)
		if len(mine) > limit {
			mine = mine[:limit]
		}
		add(Component{Type: "section", Title: "Мои задачи", Children: mine})
		if всего > len(mine) {
			add(Component{Type: "button", Actions: []Action{
				{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d из %d", len(mine), всего)}}})
		}
	}
	return nil
}

// taskRow — одна задача строкой; второй ответ — корневая ли (её
// место в своём разделе). Вынесено из цикла, чтобы выключенные задачи
// собирались и отдельно, при раскрытии ленивого раздела.
func (s *Server) taskRow(t *store.Task, byID map[int64]*store.Task, usage *screenUsage) (Component, bool) {
	{
		id := strconv.FormatInt(t.ID, 10)

		// расписание: время или «после другой задачи»
		when := t.Schedule
		if t.AfterID != 0 {
			if dep := byID[t.AfterID]; dep != nil {
				when = "после «" + dep.Title + "»"
			}
		}

		// описание — фразой, телеметрия — фактами: раньше это склеивалось
		// в один caption и обрезалось на середине
		caption := t.Prompt
		var facts []Prop
		if !t.LastRun.IsZero() {
			res := humanResult(t.LastResult)
			if len([]rune(res)) > 100 {
				res = string([]rune(res)[:100]) + "…"
			}
			facts = append(facts,
				Prop{Label: "Последний запуск", Value: t.LastRun.Format("15:04")},
				Prop{Label: "Итог", Value: res})
			if u := usage.usageBadge(engine.TaskConsumer(t)); u != "" {
				facts = append(facts, Prop{Label: "Расход", Value: u})
			}
		}

		if t.Root {
			beats, since := s.eng.Beats()
			ago := "только что"
			if since > 0 {
				ago = strings.Replace(fmt.Sprintf("%.1f сек назад", since.Seconds()), ".", ",", 1)
			}
			return Component{
				Type:      "row",
				ID:        "task-" + id,
				Layout:    "wide",
				Icon:      "waveform.path.ecg",
				IconColor: "green",
				Subtitle:  t.Title,
				Caption: "Пульс Сёмы: каждую секунду будит остальные задачи и решает, чья очередь\n" +
					fmt.Sprintf("Последний удар %s · выполнено %s", ago, plural(int(beats), "удар", "удара", "ударов")),
				Badges: []Badge{
					{Text: when, Color: "red"},
					{Text: "не выключается", Color: "gray"},
				},
			}, true
		}

		// состояние — про саму работу, а не про время последнего прохода
		backlog := s.eng.TaskBacklog(t.System)
		state, stateColor := "ждёт", "gray"
		switch {
		case !t.Enabled:
			state, stateColor = "выключена", "gray"
		case s.eng.TaskRunning(t.ID):
			state, stateColor = "работает", "green"
		case backlog > 0:
			state, stateColor = fmt.Sprintf("осталось %d", backlog), "orange"
		case t.System != "" && !t.LastRun.IsZero():
			state, stateColor = "всё сделано", "green"
		case !t.LastRun.IsZero():
			state, stateColor = "выполнена "+t.LastRun.Format("15:04"), "green"
		}
		whenColor := "blue"
		if t.AfterID != 0 {
			whenColor = "purple"
		}
		toggle := "off"
		if t.Enabled {
			toggle = "on"
		}
		warn := "Удалить задачу «" + t.Title + "»?"
		// со скриптом уходит и код задачи: человек должен знать это до нажатия
		if used := s.eng.ScriptUsedBy(t.ScriptID); len(used) > 0 && t.ScriptID != 0 {
			if sc, err := s.eng.Store().Script(t.ScriptID); err == nil && sc != nil && len(used) == 1 {
				warn += " Вместе с задачей удалится её скрипт «" + sc.Title + "»."
			}
		}
		if t.System != "" {
			warn += " Это встроенная задача — вернуть её можно кнопкой «Вернуть задачи по умолчанию»."
		}
		actions := []Action{
			{Action: "toggle_task", Target: id, Label: "toggle"},
			{Action: "edit_task_form", Target: id, Label: "Изменить"},
			{Action: "run_task", Target: id, Label: "Запустить"},
			{Action: "delete_task", Target: id, Label: "Удалить", Style: "destructive",
				ConfirmOK: "Удалить", Confirm: warn},
		}
		menu := []Action{
			{Action: "rename_task", Target: id, Label: "Переименовать",
				Prompt: "Название задачи", Value: t.Title},
		}
		if t.System == "" {
			// у встроенной задачи описание длинное и правится не здесь:
			// оно ехало в атрибут кнопки у каждой из двадцати пяти строк
			// и весило больше самой строки
			menu = append(menu, Action{Action: "retask_prompt", Target: id, Label: "Изменить описание",
				Prompt: "Что делать в этой задаче", Value: t.Prompt})
			if t.ChatID != 0 {
				menu = append(menu, Action{Action: "$goto",
					Target: "chat:" + strconv.FormatInt(t.ChatID, 10), Label: "Результаты"})
			}
		}
		return Component{
			Type:     "row",
			ID:       "task-" + id,
			Layout:   "wide",
			Subtitle: t.Title,
			Caption:  caption,
			Facts:    facts,
			Badges: []Badge{
				{Text: when, Color: whenColor},
				{Text: state, Color: stateColor},
			},
			Toggle:  toggle,
			Actions: actions,
			Menu:    menu,
		}, false
	}
}

// disabledTasksFragment — встроенные задачи, которые выключены: те же
// строки, что и в общем списке.
func (s *Server) disabledTasksFragment(_ string, _ string, _ int) ([]Component, error) {
	tasks, err := s.eng.Store().Tasks()
	if err != nil {
		return nil, err
	}
	byID := map[int64]*store.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	usage := s.buildUsage()
	var rows []Component
	for _, t := range tasks {
		if t.Root || t.Enabled || t.System == "" {
			continue
		}
		строка, _ := s.taskRow(t, byID, usage)
		rows = append(rows, строка)
	}
	return rows, nil
}

// ---- Письмо с цепочкой (разворот на месте, как в Mail/Outlook) ----

func (s *Server) buildMessage(doc *Document, msgID int64, want map[int64]bool) error {
	m, thread, bodies, err := s.eng.ThreadView(msgID, want)
	if err != nil {
		return err
	}
	// письмо читается в узкой колонке: 980 — это строка на две трети
	// экрана, глаз теряет начало следующей
	doc.Layout = "read"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	id := strconv.FormatInt(m.ID, 10)
	actions := []Action{{Action: "$goto", Target: "inbox", Label: "‹ Разбор"}}
	// «Разобраться» — вопрос про это письмо, не пересказывая его своими
	// словами в общем чате. Разговор один на письмо: заведён — открываем
	if чат := s.eng.MessageChatID(m.ID); чат > 0 {
		actions = append(actions, Action{Action: "$goto", Target: "chat:" + strconv.FormatInt(чат, 10), Label: "Открыть чат"})
	} else {
		// «Разобраться» ничего не обещало: человек не знал, что нажатие
		// заведёт разговор об этом письме (13 сентября 2026)
		actions = append(actions, Action{Action: "msg_chat", Target: id, Label: "Спросить Сёму"})
	}
	if m.Status == store.StatusPending {
		actions = append(actions,
			Action{Action: "not_spam", Target: id, Label: "Не спам", Style: "primary"},
			Action{Action: "confirm_spam", Target: id, Label: "В спам", Style: "destructive"})
	}
	// письмо — экран со своей шапкой: тема заголовком, отправитель и дата
	// подписью, решения кнопками справа
	from := m.FromName
	if from == "" {
		from = m.FromAddr
	}
	label := from
	if m.FromName != "" && m.FromAddr != "" {
		label += " · " + m.FromAddr
	}
	if !m.Date.IsZero() {
		label += " · " + m.Date.Format("02.01 15:04")
	}
	if m.Account != "" {
		label += " · " + m.Account
	}
	doc.Heading = subjectOr(m.Subject)
	doc.Subheading = label
	doc.ToolbarActions = actions

	// Порядок на экране письма — как в любом почтовом клиенте: сначала
	// само письмо, потом ответ, и только потом всё остальное. Прежде
	// сверху стояли форма ответа, «вернуться позже» и класс
	// чувствительности, а письмо — четвёртым блоком: человек открывал
	// письмо и не видел письма (13 сентября 2026).

	// само письмо: тело развёрнуто, вложения чипами под ним
	body := bodies[m.ID]
	письмо := Component{Type: "row", ID: "thr-" + id,
		Subtitle: from, Meta: m.Date.Format("02.01 15:04"), MetaInline: true,
		Detail:     body.Text,
		DetailHTML: body.HTML,
		Expanded:   true, Expandable: true,
		Avatar: initialOf(m.FromName, m.FromAddr), AvatarColor: "#8E8E93"}
	// HTML-версия показывается рамкой в песочнице: половина писем без неё
	// нечитаема — рассылка в текстовом виде это столбик слов и адресов
	if strings.TrimSpace(body.HTML) != "" {
		письмо.Frame = messageBodyAddr(m.ID, false)
		письмо.FrameImages = htmlsafe.Clean(body.HTML, false).Images
		// перевод — тот же вид письма, а не пересказ отдельным разделом:
		// меняются слова, вёрстка остаётся
		if t, ok := s.eng.MessageTranslation(m.ID); ok && strings.TrimSpace(t.HTML) != "" {
			письмо.FrameTranslated = "на «" + t.Lang + "»"
		}
	}
	// само письмо идёт без заголовка «Письмо»: на экране письма он
	// сообщает ровно ноль
	add(Component{Type: "section", ID: "тело-письма", Children: []Component{письмо}})

	// вложения сразу под письмом — они его часть, а не приложение к
	// экрану: у чека здесь виден итог, ради которого письмо и открывают
	if файлы := s.messageAttachments(m.ID); len(файлы) > 0 {
		add(Component{Type: "section", Title: "Вложения · " + strconv.Itoa(len(файлы)), Children: файлы})
	}

	// ответ. По умолчанию Сёма кладёт его в «Черновики» ящика и ничего не
	// отправляет: черновик отменяем, отправленное письмо — нет
	add(s.messageReply(m))

	if len(thread) > 1 {
		add(s.messageThread(m, thread, bodies, want))
	}

	// «Отписаться» — только когда рассылка сама сказала, как это сделать
	// (заголовок List-Unsubscribe). Гадать по ссылкам в теле нельзя: в
	// письме есть и «отписаться», и «удалить аккаунт», и они рядом
	if блок := messageUnsubscribe(m); блок != nil {
		add(*блок)
	}

	// Дальше — то, что про письмо, а не само письмо: что с ним сделал
	// Сёма, напоминание и класс чувствительности. Под свёрткой, потому
	// что открывают письмо ради письма, а не ради его свойств
	хвост := []Component{s.whatSemaDid(m)}
	хвост = append(хвост, Component{
		Type: "row", Layout: "wide", Icon: "clock",
		Subtitle: messageReminder(s, m.ID),
		Caption:  "Сёма напомнит об этом письме в назначенный час — записью в «Уведомлениях». Само письмо останется на месте: прятать почту, чтобы она не мешала, — верный способ её потерять",
		Selects: []Select{{
			Name: "когда", Label: "Напомнить", Value: "—",
			Options: []string{"—", "через час", "сегодня вечером", "завтра", "в понедельник", "через неделю", "через месяц"},
			Action:  Action{Action: "snooze_msg", Target: id},
		}},
	})
	хвост = append(хвост, s.messageClass(m))
	add(Component{Type: "section", ID: "про-письмо", Title: "Что Сёма о нём знает",
		Collapsed: true, Children: хвост})
	return nil
}

// messageClass — сегмент класса и одна строка про то, что уходит наружу.
func (s *Server) messageClass(m *store.Message) Component {
	class := s.eng.ClassOfMessage(m)
	кто := "Сёма решил так сам по приметам письма"
	if mark, ok := s.eng.Store().ClassMarkOf(store.ClassMsg, m.ID); ok {
		if mark.By != "" {
			кто = "Пометили вы — примета этого не перебьёт"
		} else if mark.Why != "" {
			// «почему секретное» раньше не отвечалось ничем: хранился один
			// итог, и разбирать приходилось пересчётом по коду
			кто = "Сёма решил так по примете: " + mark.Why
		}
	}
	что := "Обычное письмо: Сёма показывает его любой модели, в том числе облачной."
	switch class {
	case store.ClassSensitive:
		что = "Чувствительное уходит облачной модели по нынешней настройке; секретное не уходит никогда."
		if s.eng.SensitiveMode() == "ask" {
			что = "Чувствительное уходит облачной модели только с вашего согласия по отправителю; секретное не уходит никогда."
		}
	case store.ClassSecret:
		что = "Секретное не уходит облачной модели никогда — только своей и доверенной."
	}
	c := Component{Type: "classpicker", ID: "класс-" + strconv.FormatInt(m.ID, 10),
		Icon: "lock", Title: capitalized(class), Text: class,
		Caption: кто + ". " + что}
	target := store.ClassMsg + "~" + strconv.FormatInt(m.ID, 10) + "~"
	for _, name := range []string{store.ClassPlain, store.ClassSensitive, store.ClassSecret} {
		c.Actions = append(c.Actions, Action{Action: "mark_class", Target: target + name, Label: name})
	}
	return c
}

// messageAttachments — файлы письма строками: у чека виден его итог.
func (s *Server) messageAttachments(msgID int64) []Component {
	atts, err := s.eng.Store().AttachmentsOfMessage(msgID)
	if err != nil {
		return nil
	}
	var out []Component
	for _, a := range atts {
		row := Component{Type: "row", Layout: "wide", ID: "att-" + strconv.FormatInt(a.ID, 10),
			Icon: "doc", Subtitle: a.Filename, Caption: humanBytes(a.Size),
			Actions: []Action{{Action: "$file", Target: strconv.FormatInt(a.ID, 10), Label: "Открыть"}}}
		if a.Kind == store.AttKindReceipt && a.Amount > 0 {
			row.Caption = humanBytes(a.Size) + " · Сёма прочитал и завёл чек"
			row.Badges = []Badge{{Text: rubles(a.Amount), Level: LevelInfo}}
		}
		out = append(out, row)
	}
	return out
}

// messageThread — одиннадцать одинаковых счетов от одного отправителя это
// не одиннадцать строк, а шкала с датами и три письма: свежее, это и
// старое. Остальные — по кнопке, и она разворачивает их на месте.
func (s *Server) messageThread(m *store.Message, thread []*store.Message, bodies map[int64]store.Body, want map[int64]bool) Component {
	порядок := append([]*store.Message{}, thread...)
	sort.SliceStable(порядок, func(i, j int) bool { return порядок[i].Date.Before(порядок[j].Date) })
	c := Component{Type: "chain", ID: "цепочка-" + strconv.FormatInt(m.ID, 10), Title: "Цепочка"}
	для := plural(len(порядок), "письмо", "письма", "писем")
	if первое, последнее := порядок[0], порядок[len(порядок)-1]; !первое.Date.IsZero() && !последнее.Date.IsZero() {
		для += " · с " + dayMonth(первое.Date) + " по " + dayMonth(последнее.Date)
	}
	c.Meta = для
	for _, tm := range порядок {
		клетка := Cell{Title: tm.Date.Format("02.01")}
		if tm.ID == m.ID {
			клетка.State = "on"
		}
		c.Cells = append(c.Cells, клетка)
	}
	// показываем открытое письмо, то, что человек развернул сам, и края
	// цепочки: остальное прячется за кнопкой
	показ := map[int64]bool{m.ID: true}
	for id := range want {
		показ[id] = true
	}
	показ[порядок[0].ID] = true
	показ[порядок[len(порядок)-1].ID] = true
	c.Caption = "Одна тема на все письма. Открыто письмо от " + dayMonth(m.Date) + "; остальные — строкой ниже."
	var спрятаны []string
	for i := len(порядок) - 1; i >= 0; i-- {
		tm := порядок[i]
		if !показ[tm.ID] {
			спрятаны = append(спрятаны, strconv.FormatInt(tm.ID, 10))
			continue
		}
		мета := tm.Date.Format("02.01 15:04")
		if tm.Account != "" && tm.Account != m.Account {
			мета += " · " + tm.Account
		}
		// тема в цепочке пишется целиком, а не «то же самое»: три строки
		// с одинаковой темой читаются как одно письмо, показанное трижды,
		// только когда у них нет заголовка вовсе
		тема := subjectInThread(tm.Subject, m.Subject)
		if тема == "" {
			тема = subjectOr(tm.Subject)
		}
		row := Component{Type: "row", ID: "thr-" + strconv.FormatInt(tm.ID, 10),
			Subtitle: тема, Meta: мета, MetaInline: true,
			Caption: clipRunes(strings.TrimSpace(tm.Snippet), 120)}
		if tm.ID == m.ID {
			row.Subtitle = тема + " · это письмо"
			row.Caption = "открыто выше"
		} else {
			row.Actions = []Action{{Action: "$goto", Target: "msg:" + strconv.FormatInt(tm.ID, 10), Label: "Открыть"}}
			if b, ok := bodies[tm.ID]; ok && want[tm.ID] {
				row.Detail, row.DetailHTML, row.Expanded, row.Expandable = b.Text, b.HTML, true, true
			}
		}
		c.Children = append(c.Children, row)
	}
	if len(спрятаны) > 0 {
		// разворачиваем на месте: адрес письма несёт список развёрнутых.
		// Переход — строкой, а не кнопкой цепочки: по строке кликается вся
		// её ширина, и на телефоне в неё легче попасть
		c.Children = append(c.Children, Component{Type: "row", Layout: "wide", Icon: "envelope",
			Subtitle: "Ещё " + plural(len(спрятаны), "письмо", "письма", "писем"),
			Caption:  "развернутся здесь же, вместе с текстом",
			Actions: []Action{{Action: "$goto",
				Target: "msg:" + strconv.FormatInt(m.ID, 10) + "~" + strings.Join(спрятаны, ","),
				Label:  "Развернуть"}}})
	}
	return c
}

// whatSemaDid — что с письмом уже произошло. Человек открывает письмо
// не только чтобы прочитать: он хочет знать, разобрано оно или ждёт его.
func (s *Server) whatSemaDid(m *store.Message) Component {
	st := s.eng.Store()
	sec := Component{Type: "section", Title: "Что с ним сделал Сёма"}
	строка := func(icon, text, пояснение string) {
		sec.Children = append(sec.Children, Component{Type: "row", Layout: "wide",
			Icon: icon, Subtitle: text, Caption: пояснение})
	}
	вердикт := strings.TrimSpace(m.Reason)
	if вердикт != "" {
		вердикт = "Сёма: " + вердикт + verdictSuffix(m)
	}
	switch m.Status {
	case store.StatusPending:
		строка("exclamationmark.triangle", "Похоже на спам — ждёт вашего решения", вердикт)
	case store.StatusHam:
		строка("checkmark", "Вы сказали «не спам» — отправитель в белом списке", вердикт)
	case store.StatusConfirmed, store.StatusQueued:
		строка("trash", "Перенёс в спам на почтовом сервере по вашему решению", вердикт)
	case store.StatusAutoMoved:
		строка("trash", "Перенёс в спам сам — по выученному правилу отправителя", вердикт)
	case store.StatusPurged:
		строка("trash", "Удалил из папки спама на сервере по сроку хранения", вердикт)
	case store.StatusArchived:
		строка("archivebox", "Разобрал из папки ящика и уложил в архив на сервере", вердикт)
	case store.StatusServerSpam:
		строка("exclamationmark.shield",
			"Спамом его посчитал почтовый сервер, а не Сёма. Письмо прочитано на месте: Сёма его не судил и никуда не переносил", вердикт)
	case store.StatusServerTrash:
		строка("trash", "Лежит в корзине почтового сервера — это ваше решение. Сёма прочитал письмо на месте и ничего не трогал", вердикт)
	default:
		строка("tray.full", "Оставил в разборе — это не спам", вердикт)
	}
	if rules, err := st.Rules(); err == nil {
		switch senderRule(rules, m.FromAddr) {
		case "ham":
			строка("checkmark", "Отправитель в белом списке — его письма Сёма не судит", "")
		case "spam":
			строка("exclamationmark.triangle", "Отправитель в чёрном списке — его письма уезжают в спам сами", "")
		}
	}
	if g := s.eng.DigestGroupOf(m); g != "" {
		name := g
		if groups, err := st.DigestGroups(); err == nil {
			for _, гр := range groups {
				if гр.Name == g && гр.Title != "" {
					name = гр.Title
				}
			}
		}
		строка("envelope", "Положил в группу «"+name+"» на «Почте»", "")
	}
	if m.MovedAt != nil {
		строка("clock", "Перенос на сервере "+whenText(m.MovedAt.Format(time.RFC3339)), "")
	}
	sec.Children = append(sec.Children, Component{Type: "text", Style: "secondary",
		Text: "Из архива Сёмы письма не удаляются никогда — «В спам» касается только почтового сервера."})
	return sec
}

// senderRule — что человек уже решил про этот адрес: «ham» —
// белый список, «spam» — чёрный, пусто — решения не было.
func senderRule(rules []*store.Rule, addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	домен := addr
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		домен = addr[i+1:]
	}
	for _, r := range rules {
		v := strings.ToLower(strings.TrimSpace(r.Value))
		if (r.Kind == "sender" && v == addr) || (r.Kind == "domain" && v == домен) {
			return r.Action
		}
	}
	return ""
}

// dayMonth — «9 сентября»: даты цепочки читаются словами, а «05» и «09»
// рядом человек путает.
func dayMonth(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return fmt.Sprintf("%d %s", t.Day(), monthNames[int(t.Month())-1])
}

// capitalized — первая буква прописной по руне. По байту «чувствительное»
// разрезается посреди буквы, и в заголовке стоит мусор.
func capitalized(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// rubles — «2 165,76 ₽»: точка вместо запятой и слипшиеся тысячи читаются
// как чужие деньги.
func rubles(v float64) string {
	целые := int64(v)
	дробь := int64((v-float64(целые))*100 + 0.5)
	знаки := strconv.FormatInt(целые, 10)
	var b strings.Builder
	for i, r := range знаки {
		if i > 0 && (len(знаки)-i)%3 == 0 {
			b.WriteRune('\u00a0')
		}
		b.WriteRune(r)
	}
	return fmt.Sprintf("%s,%02d ₽", b.String(), дробь)
}

// secondsText — «0,01 с»: русский текст пишет дробь запятой.
func secondsText(v float64, знаков int) string {
	return strings.Replace(strconv.FormatFloat(v, 'f', знаков, 64), ".", ",", 1)
}

func verdictSuffix(m *store.Message) string {
	if m.Confidence > 0 {
		return fmt.Sprintf(" (спам %.0f%%)", m.Confidence*100)
	}
	return ""
}

func initialOf(name, addr string) string {
	src := name
	if src == "" {
		src = addr
	}
	for _, r := range src {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// ---- Отправители (вкладка раздела «Почта») ----

// отсев — ящики, закрытые для ЭТОЙ сборки (снимок на устройстве, см.
// buildInbox); влияет только на историю писем раскрытого отправителя —
// сам список правил «спам/бел. список» не привязан к одному ящику.
func (s *Server) buildSenders(doc *Document, filter, expand, search string, отсев *store.Exclusion, limit int) error {
	st := s.eng.Store()
	spamN, _ := st.CountRules("spam")
	hamN, _ := st.CountRules("ham")
	doc.Searchable = true
	doc.Filters = []Filter{
		{ID: "spam", Title: fmt.Sprintf("В спаме · %d", spamN)},
		{ID: "ham", Title: fmt.Sprintf("Белый список · %d", hamN)},
	}
	if filter != "ham" {
		filter = "spam"
	}
	doc.ActiveFilter = filter

	add := func(c Component) { doc.Components = append(doc.Components, c) }
	doc.Heading = "Почта"
	doc.Subheading = "Кого Сёма отправляет в спам — можно вернуть вместе со всеми письмами"
	doc.Notice = &Notice{Icon: "archivebox",
		Text: "Файлы и чеки Сёма забирает себе до того, как письмо уедет в спам, — они остаются навсегда. Возврат нужен, только если письма этого отправителя хочется снова видеть во «Входящих»"}

	senders, err := st.Senders(filter)
	if err != nil {
		return err
	}
	// поиск — на сервере, по имени и адресу: строка поиска над списком
	// иначе искала бы только среди показанных
	if search != "" {
		var подходят []*store.Sender
		for _, sn := range senders {
			if looksLike(search, sn.Name, sn.Addr) {
				подходят = append(подходят, sn)
			}
		}
		senders = подходят
	}
	// Первая страница — двадцать пять. Все двести отправителей разом это
	// 5 323 узла и 462 КБ (замер 12 сентября 2026), а смотрят обычно
	// верх списка. Раскрытый отправитель попадает на страницу всегда
	всегоОтправителей := len(senders)
	if limit <= 0 || limit == defaultRows {
		limit = 25
	}
	if expand != "" {
		for i, sn := range senders {
			if sn.Addr == expand && i >= limit {
				limit = i + 1
			}
		}
	}
	if len(senders) > limit {
		senders = senders[:limit]
	}
	withReceipts := s.eng.SendersWithReceipts()
	doc.ToolbarActions = append(doc.ToolbarActions, Action{
		Action: "allow_sender", Label: "Разрешить адрес",
		Prompt: "Адрес, письма которого всегда оставлять во «Входящих»",
	})
	if filter == "spam" {
		n := 0
		for _, sn := range senders {
			if withReceipts[sn.Addr] > 0 {
				n++
			}
		}
		if n > 0 {
			doc.ToolbarActions = append(doc.ToolbarActions, Action{
				Action: "restore_receipts", Label: fmt.Sprintf("Вернуть с чеками (%d)", n), ConfirmOK: "Вернуть",
				Confirm: fmt.Sprintf("Вернуть из спама %s, у которых есть чеки и счета? Их письма переедут во «Входящие».",
					pluralRu(n, "отправителя", "отправителей", "отправителей")),
			})
		}
	}
	if len(senders) == 0 {
		if filter == "ham" {
			add(emptyState("Белый список пуст",
				"сюда попадают адреса, письма которых Сёма никогда не отправит в спам · «Разрешить адрес» вверху заводит первый", "checkmark.shield"))
		} else {
			add(emptyState("В спаме никого нет",
				"Сёма отправляет отправителя в спам, когда вы подтверждаете это на «Почте» · пока таких решений не было", "tray"))
		}
		return nil
	}
	rows := make([]Component, 0, len(senders)*2)
	for _, sn := range senders {
		name := sn.Name
		if name == "" {
			name = sn.Addr
		}
		// счётчики — парами «подпись — значение», а не склейкой через «·»:
		// «214 писем · последнее 3 сентября · есть чеки: 2» набиралось
		// одним кеглем, и глаз не отделял число от слова
		факты := []Prop{{Label: "Писем", Value: strconv.Itoa(sn.Count)}}
		if !sn.LastDate.IsZero() {
			факты = append(факты, Prop{Label: "Последнее", Value: dayLine(sn.LastDate)})
		}
		var hash uint32
		for _, b := range []byte(sn.Addr) {
			hash = hash*31 + uint32(b)
		}
		// имя и адрес в одну линию, мета — второй строкой: адрес третьей
		// строкой растягивал строку до восьмидесяти точек на каждого из
		// двух сотен отправителей
		row := Component{
			Type:        "row",
			ID:          "snd-" + sn.Addr,
			Subtitle:    name,
			Meta:        sn.Addr,
			MetaInline:  true,
			Facts:       факты,
			Avatar:      initialOf(sn.Name, sn.Addr),
			AvatarColor: avatarPalette[hash%uint32(len(avatarPalette))],
			Expandable:  true,
			Expanded:    sn.Addr == expand,
		}
		if sn.Addr == expand {
			// раскрытая строка подсвечена, а клик по ней складывает
			// историю обратно — иначе выхода из раскрытия нет
			row.Tint = "blue"
			row.Actions = []Action{{Action: "$goto", Target: "senders", Label: "Свернуть"}}
		}
		if n := withReceipts[sn.Addr]; n > 0 {
			row.Facts = append(row.Facts, Prop{Label: "Чеки", Value: strconv.Itoa(n)})
		}
		if filter == "spam" {
			// «в спаме» у каждой из двухсот строк на вкладке «В спаме» —
			// это подпись вкладки, а не примета строки: норма молчит
			row.Actions = append(row.Actions, Action{Action: "restore_sender", Target: sn.Addr, Label: "Вернуть из спама"})
			// второе действие есть только здесь: на белом списке «Удалить
			// правило» и «Убрать из белого списка» — одно и то же, и меню
			// «…» открывалось ради двойника
			row.Menu = []Action{{Action: "forget_sender", Target: sn.Addr,
				Label: "Удалить правило", Style: "destructive", ConfirmOK: "Удалить",
				Confirm: "Убрать правило для " + sn.Addr + "? Письма останутся там, где лежат."}}
		} else {
			// без пометки «разрушительное»: это единственное действие
			// строки, а разрушительное общий проход уводит в меню — и
			// строка осталась бы вовсе без кнопки
			row.Actions = append(row.Actions, Action{Action: "forget_sender", Target: sn.Addr,
				Label: "Убрать из белого списка", ConfirmOK: "Убрать",
				Confirm: "Убрать правило для " + sn.Addr + "? Письма останутся там, где лежат."})
		}
		// история писем живёт в раскрытии самой строки, а не отдельными
		// строками следом: иначе непонятно, чьи это письма
		if sn.Addr == expand {
			hist, err := st.MessagesFrom(sn.Addr, отсев, 30)
			if err != nil {
				return err
			}
			children := []Component{{Type: "group", Text: "История писем"}}
			for _, m := range hist {
				subject := m.Subject
				if subject == "" {
					subject = "(без темы)"
				}
				children = append(children, Component{
					Type:       "row",
					ID:         "hist-" + strconv.FormatInt(m.ID, 10),
					Subtitle:   subject,
					Meta:       dayLine(m.Date) + " " + m.Date.Format("15:04") + " · " + statusLabel(m.Status),
					MetaInline: true,
					Actions:    []Action{{Action: "$goto", Target: "msg:" + strconv.FormatInt(m.ID, 10), Label: "Открыть"}},
				})
			}
			if len(hist) == 0 {
				children = append(children, Component{Type: "text", Style: "secondary",
					Text: "Писем этого отправителя в архиве нет: правило есть, а писем ещё не приходило."})
			}
			children = append(children, Component{Type: "text", Style: "secondary", Text: fmt.Sprintf(
				"«Вернуть» — значит убрать правило и перенести %s обратно во «Входящие».",
				plural(sn.Count, "письмо", "письма", "писем"))})
			row.Children = children
		}
		rows = append(rows, row)
	}
	// раздел без заголовка: счётчик стоит в фильтре, а «Отправители (214)»
	// над списком отправителей ничего не добавляло
	add(Component{Type: "section", ID: "senders-list", Children: rows})
	if всегоОтправителей > len(rows) {
		add(Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d из %d", len(rows), всегоОтправителей)}}})
	}
	return nil
}

func statusLabel(status string) string {
	switch status {
	case store.StatusConfirmed, store.StatusAutoMoved:
		return "в спаме"
	case store.StatusQueued:
		return "переносится"
	case store.StatusPurged:
		return "удалено из почты, копия у Сёмы"
	case store.StatusHam:
		return "не спам"
	case store.StatusPending:
		return "ждёт подтверждения"
	default:
		return "во входящих"
	}
}

// pluralRu — короткий вариант склонения для подсказок.
func pluralRu(n int, one, few, many string) string {
	mod10, mod100 := n%10, n%100
	switch {
	case mod10 == 1 && mod100 != 11:
		return fmt.Sprintf("%d %s", n, one)
	case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
		return fmt.Sprintf("%d %s", n, few)
	default:
		return fmt.Sprintf("%d %s", n, many)
	}
}

// splitRows делит строки на встроенные задачи и заведённые человеком.
func splitRows(tasks []*store.Task, rows []Component) (sys, mine []Component) {
	system := map[string]bool{}
	for _, t := range tasks {
		if t.System != "" {
			system["task-"+strconv.FormatInt(t.ID, 10)] = true
		}
	}
	for _, r := range rows {
		if system[r.ID] {
			sys = append(sys, r)
		} else {
			mine = append(mine, r)
		}
	}
	return sys, mine
}

// taskWizard собирает окно правки задачи — те же шаги, что при создании.
func (s *Server) taskWizard(tasks []*store.Task, title, prompt, when string, submit Action, header string) *Wizard {
	names := []string{"— по времени, без очереди"}
	for _, t := range tasks {
		if !t.Root && t.Title != title {
			names = append(names, t.Title)
		}
	}
	after := names[0]
	schedule := when
	if strings.HasPrefix(when, "после") {
		after = strings.Trim(strings.TrimPrefix(when, "после"), " «»")
		schedule = ""
	}
	what := title
	if prompt != "" {
		what = prompt
	}
	return &Wizard{
		Title: header,
		Steps: []WizardStep{
			{
				Title: "Что делать",
				Hint:  "Опишите словами — это же станет названием задачи",
				Fields: []Field{
					{Type: "text", Name: "what", Label: "Что сделать", Value: what},
				},
			},
			{
				Title: "Когда",
				Hint:  "Выберите готовое расписание или напишите своё",
				Fields: []Field{
					{Type: "chips", Name: "preset", Label: "Как запускать",
						Options: []string{"каждые 30 сек", "каждые 5 мин", "каждые 30 мин", "каждый час", "9:00"}},
					{Type: "text", Name: "when", Label: "Точное расписание", Value: schedule,
						Hint: "Своими словами: «каждые 15 мин», «каждое вс 20:00», «30.09 10:00»"},
					{Type: "picker", Name: "after", Label: "Запускать после задачи",
						Value: after, Options: names,
						Hint: "Задача стартует сразу после того, как выбранная закончит проход"},
				},
			},
			{Title: "Проверка", Hint: "Проверьте и сохраните", Fields: []Field{}},
		},
		Summary: "«{what}» · {when}",
		Submit:  submit,
		Cancel:  Action{Action: "close_task_form", Label: "Отмена"},
	}
}

// ---- Скрипты ----

const scriptSample = `// пример: траты по чекам за месяц — чек это файл с суммой
const since = sema.date('начало месяца');
const list = sema.files({ kind: 'receipt', limit: 500 })
  .filter(f => new Date(f.date) >= since);
const byVendor = {};
for (const f of list) byVendor[f.vendor] = (byVendor[f.vendor] || 0) + f.amount;

const rows = Object.entries(byVendor).sort((a, b) => b[1] - a[1]);
const total = rows.reduce((s, r) => s + r[1], 0);
rows.forEach(r => console.log(r[0] + ' — ' + r[1] + ' ₽'));
return rows.length + ' продавцов, всего ' + total + ' ₽';`

func (s *Server) buildScripts(doc *Document, scriptID int64) error {
	st := s.eng.Store()
	scripts, err := st.Scripts()
	if err != nil {
		return err
	}
	doc.Heading = "Скрипты"
	doc.Subheading = "JavaScript выполняется внутри Сёмы — на вашем Mac, без интернета, если скрипт сам его не попросит"
	doc.PanelTitle = "Скрипты"
	doc.PanelAction = &Action{Action: "new_script", Label: "Новый скрипт"}
	// удаление — на экране скрипта, а не у каждого пункта панели: 88 меню
	// с вопросом «Удалить?» весили треть экрана (perf-pass 5); задачи —
	// одним проходом, а не запросом на скрипт
	users := s.eng.ScriptUsers()
	for _, sc := range scripts {
		meta := "не запускался"
		if !sc.LastRun.IsZero() {
			meta = fmt.Sprintf("%s · %.2f сек", sc.LastRun.Format("15:04"), sc.LastMs)
		}
		if used := users[sc.ID]; len(used) > 0 {
			meta += " · задача «" + strings.Join(used, "», «") + "»"
		}
		doc.Panel = append(doc.Panel, PanelItem{
			ID: "script:" + strconv.FormatInt(sc.ID, 10), Title: sc.Title, Subtitle: meta,
		})
	}
	if scriptID == 0 && len(scripts) > 0 {
		scriptID = scripts[0].ID
	}
	if scriptID == 0 {
		doc.ToolbarActions = []Action{{Action: "new_script", Label: "Новый скрипт", Style: "primary"}}
		doc.Components = append(doc.Components, emptyState("Скриптов пока нет",
			"скрипт — несколько строк JavaScript, которые Сёма выполняет у себя: собрать письма за сутки, "+
				"сходить на сервер, посчитать чеки. Проще всего попросить Сёму написать его в чате — "+
				"или завести пустой кнопкой «Новый скрипт»", "curlybraces"))
		return nil
	}
	doc.ActivePanel = "script:" + strconv.FormatInt(scriptID, 10)

	sc, err := st.Script(scriptID)
	if err != nil {
		return err
	}
	id := strconv.FormatInt(sc.ID, 10)
	doc.Heading = sc.Title
	doc.ToolbarActions = []Action{
		{Action: "rename_script", Target: id, Label: "Переименовать",
			Prompt: "Название скрипта", Value: sc.Title},
		{Action: "script_to_task", Target: id, Label: "Сделать задачей",
			Prompt: "Когда запускать: «каждый час», «9:00», «после «Архив писем»»", Value: "9:00"},
		{Action: "run_script", Target: id, Label: "Запустить", Style: "primary"},
	}
	used := s.eng.ScriptUsedBy(sc.ID)
	// скрипт задачи сам по себе не удаляется: задача осталась бы без кода и
	// падала по расписанию — кнопки нет, а почему — сказано ниже
	if len(used) == 0 {
		doc.ToolbarActions = append(doc.ToolbarActions, Action{Action: "delete_script", Target: id,
			Label: "Удалить скрипт", Style: "destructive", ConfirmOK: "Удалить",
			Confirm: "Удалить скрипт «" + sc.Title + "»?"})
	}
	if len(used) > 0 {
		doc.Subheading = "запускает задача «" + strings.Join(used, "», «") + "» · удалить можно только вместе с ней"
		doc.Components = append(doc.Components, Component{Type: "text", Style: "secondary",
			Text: "Этот скрипт — код задачи «" + strings.Join(used, "», «") + "». Отдельно он не удаляется: задача осталась бы без кода и падала бы по расписанию. Удалите задачу — она заберёт скрипт с собой."})
	}
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	s.mu.Lock()
	run := s.scriptRuns[sc.ID]
	s.mu.Unlock()

	add(Component{
		Type: "form",
		ID:   "script-" + id,
		Fields: []Field{
			{Type: "code", Name: "code", Label: "Код", Value: sc.Code, ErrorLine: run.ErrLine},
		},
		Submit: &Action{Action: "save_script", Target: id, Label: "Сохранить"},
	})
	add(Component{Type: "text", Style: "secondary",
		Text: "Доступно: sema.messages() · sema.files() · sema.ask() · sema.llm() · sema.chat() · sema.http.get() · sema.store · sema.task() · sema.date() · sema.period() · инструменты модулей (sema.attention.remind) · console.log()"})

	if len(run.Log) > 0 || run.ErrText != "" || run.Value != "" {
		rows := make([]Component, 0, len(run.Log)+3)
		for _, line := range run.Log {
			rows = append(rows, Component{Type: "text", Style: "secondary", Text: line})
		}
		if run.ErrText != "" {
			msg := "✗ " + run.ErrText
			if run.ErrLine > 0 {
				msg = fmt.Sprintf("✗ строка %d: %s", run.ErrLine, run.ErrText)
			}
			rows = append(rows, Component{Type: "text", Style: "error", Text: msg})
			if run.ErrCode != "" {
				rows = append(rows, Component{Type: "text", Style: "secondary", Text: "   " + run.ErrCode})
			}
		} else if run.Value != "" {
			rows = append(rows, Component{Type: "text", Text: "→ " + run.Value})
		}
		// «Вывод · 15:04:05 · 0.12 сек» — три разных числа в заголовке
		// одним кеглем; время и секунды уехали в строку чисел
		итог := "выполнен"
		if run.ErrText != "" {
			итог = "остановлен"
		}
		add(Component{Type: "figs", ID: "script-figs", Children: []Component{
			{Text: run.At.Format("15:04:05"), Title: "запуск"},
			{Text: fmt.Sprintf("%.2f", run.Seconds), Unit: "с", Title: итог},
			{Text: strconv.Itoa(len(run.Log)), Title: "строк вывода"},
		}})
		add(Component{Type: "section", ID: "script-out", Title: "Вывод", Children: rows})
	} else if sc.LastResult != "" {
		add(Component{Type: "section", Title: "Последний запуск", Children: []Component{
			{Type: "text", Style: "secondary", Text: sc.LastResult},
		}})
	}
	// История вызовов: «последний запуск» отвечает, что было сейчас, а по
	// нему одному скрипт, переставший работать месяц назад, выглядит живым
	// (13 сентября 2026)
	s.callHistory(add, "script:"+strconv.FormatInt(sc.ID, 10))
	return nil
}

// callHistory — когда потребителя (скрипт, модуль, задачу) звали и чем
// это кончилось. Двадцать последних записей журнала расхода: больше —
// уже разбор на «Здоровье», меньше — не история.
func (s *Server) callHistory(add func(Component), consumer string) {
	rows, err := s.eng.Store().UsageOfConsumer(consumer, 20)
	if err != nil || len(rows) == 0 {
		add(Component{Type: "section", Title: "История вызовов", Children: []Component{
			emptyState("Вызовов пока не было",
				"здесь появятся запуски: когда, сколько заняло и чем кончилось", "clock")}})
		return
	}
	var kids []Component
	for _, r := range rows {
		итог, level := "выполнен", ""
		if !r.OK {
			итог, level = "ошибка", LevelAlert
		}
		строка := Component{Type: "row", Layout: "wide",
			ID:       "вызов-" + strconv.FormatInt(r.ID, 10),
			Subtitle: r.At.Format("02.01 15:04:05") + " · " + итог,
			Caption:  r.Note,
			Facts: []Prop{
				{Label: "Заняло", Value: fmt.Sprintf("%.2f с", r.Seconds)},
			}}
		if r.Model != "" {
			строка.Facts = append(строка.Facts, Prop{Label: "Модель", Value: r.Model})
		}
		if r.TokensIn+r.TokensOut > 0 {
			строка.Facts = append(строка.Facts, Prop{Label: "Токенов", Value: strconv.Itoa(r.TokensIn + r.TokensOut)})
		}
		withLevel(&строка, level)
		kids = append(kids, строка)
	}
	last := rows[0]
	мета := "последний — " + whenText(last.At.Format(time.RFC3339))
	if !last.OK {
		мета += " · с ошибкой"
	}
	add(Component{Type: "section", ID: "история-вызовов", Title: "История вызовов · " + strconv.Itoa(len(rows)),
		Collapsed: true, Children: kids, Meta: мета})
}

// toolCardComponent — карточка скрипта в переписке: код, результат и
// возможность открыть его или повесить на расписание. Ничего не
// выполняется втихую: карточка остаётся в истории чата.
// toolCardComponent — карточка дела в ленте: то, что Сёма сделал руками, а
// не сказал словами. Значок, заголовок, мета, полоска уровня у ждущих, тело
// моноширинным или прозой и ряд действий, где разрушительное отделено
// пустотой. Строкой с раскрытием такое читалось как пункт настроек, и
// команду на подтверждение пролистывали мимо.
func (s *Server) toolCardComponent(card engine.ToolCard, at string, msgID int64) Component {
	switch {
	case card.Memory != nil:
		return memoryCardComponent(card, at)
	case card.SSH != nil:
		return s.sshCardComponent(card, at)
	case card.MCP != nil:
		return s.mcpCardComponent(card, at)
	case card.TG != nil:
		// состояние живёт в исходящих: черновик, закрытый после отправки
		// другого варианта, не должен показывать кнопки по старому снимку
		if card.TG.ID > 0 {
			if o, ok := s.eng.Store().TGOutboxByID(card.TG.ID); ok {
				switch o.State {
				case "sent":
					card.TG.Status = "sent"
				case "rejected":
					card.TG.Status, card.TG.Why = "rejected", o.Note
				}
			}
		}
		return tgCardComponent(card, at)
	case card.Skill != nil:
		return s.skillCardComponent(card, at)
	case card.Assign != nil:
		return s.assignCardComponent(card, at)
	case card.Share != nil:
		return s.shareCardComponent(card, at)
	case card.Done != nil:
		c := Component{Type: "toolcard", Icon: "checkmark.seal.fill",
			Title: "Вопрос закрыт", Meta: "Сёма · " + at,
			Text:    card.Done.Summary,
			Caption: "Чат живёт ещё час и уходит в память.",
			Badges:  []Badge{{Text: "закрыт", Level: LevelOK}}}
		withLevel(&c, LevelOK)
		return c
	case card.Timer != nil:
		return timerCardComponent(card, at)
	case card.Rule != "":
		return ruleCardComponent(card, at, msgID)
	case strings.HasPrefix(card.Title, "Поиск · "):
		return searchCardComponent(card, at)
	}
	return scriptCardComponent(card, at)
}

// scriptCardComponent — запуск скрипта из переписки: код моноширинным,
// итог тихой строкой под ним.
func scriptCardComponent(card engine.ToolCard, at string) Component {
	c := Component{Type: "toolcard", Icon: "chevron.left.forwardslash.chevron.right",
		Title: "Скрипт «" + card.Title + "»", Meta: at,
		Detail:  card.Code,
		Badges:  []Badge{{Text: "выполнен за " + secondsText(card.Seconds, 2) + " с", Level: LevelOK}},
		Caption: "Итог: " + clipRunes(humanResult(card.Result), 400)}
	if card.Err != "" {
		c.Badges = []Badge{{Text: "ошибка", Level: LevelAlert}}
		c.Caption = card.Err
		withLevel(&c, LevelAlert)
	}
	if strings.TrimSpace(card.Result) == "" && card.Err == "" {
		c.Caption = ""
	}
	if card.ScriptID != 0 {
		id := strconv.FormatInt(card.ScriptID, 10)
		c.Actions = []Action{
			{Action: "script_to_task", Target: id, Label: "Сделать задачей",
				Prompt: "Когда запускать: «каждый час», «9:00», «после «Архив писем»»", Value: "9:00"},
		}
		// переход у карточки дела живёт в «…»: кнопка перехода в ряду
		// действий читается как поручение Сёме, а это всего лишь адрес
		c.Menu = []Action{{Action: "$goto", Target: "script:" + id, Label: "Открыть скрипт"}}
	}
	return c
}

// timerCardComponent — таймер чата: ждёт с обратным счётом или сработал.
func timerCardComponent(card engine.ToolCard, at string) Component {
	c := Component{Type: "toolcard", Icon: "timer", Title: "Таймер",
		Meta: "поставил Сёма · " + at}
	note := strings.TrimSpace(card.Timer.Note)
	switch card.Timer.Status {
	case "fired":
		c.Title = "Таймер сработал"
		c.Text = note
		c.Caption = "Сёма посмотрел сам и написал в этот чат."
		c.Badges = []Badge{{Text: "сработал", Level: LevelOK}}
		withLevel(&c, LevelOK)
		return c
	}
	when, past := card.Timer.At, false
	if t, err := time.Parse(time.RFC3339, card.Timer.At); err == nil {
		when = t.Format("15:04")
		past = t.Before(time.Now())
	}
	c.Meta = "поставил Сёма · до " + when
	if note != "" {
		c.Caption = "Что проверить: «" + note + "». По сроку Сёма посмотрит сам и напишет сюда."
	}
	if past {
		c.Badges = []Badge{{Text: "прошёл · " + when}}
		return c
	}
	c.Badges = []Badge{{Text: "ждёт", Level: LevelInfo}}
	c.Until = card.Timer.At // клиент дорисует обратный отсчёт
	if card.Timer.TaskID > 0 {
		c.Actions = []Action{{Action: "delete_task", Target: strconv.FormatInt(card.Timer.TaskID, 10), Label: "Отменить"}}
	}
	return c
}

// ruleCardComponent — предложенное правило для инструкции модуля.
func ruleCardComponent(card engine.ToolCard, at string, msgID int64) Component {
	c := Component{Type: "toolcard", Icon: "sparkles", Title: card.Title, Meta: at,
		Text: card.Code}
	if card.RuleDone {
		// ответ виден в самой карточке: без него человек жал кнопку
		// снова и снова, и правило ложилось в инструкцию трижды
		withLevel(&c, LevelOK)
		c.Badges = []Badge{{Text: "записано", Level: LevelOK}}
		c.Caption = card.Result
		return c
	}
	withLevel(&c, LevelWarn)
	c.Badges = []Badge{{Text: "ждёт вас", Level: LevelWarn}}
	c.Caption = "Правило попадёт в инструкцию модуля и будет работать впредь."
	// цель — номер сообщения, а не текст: правило бывает в тысячу знаков
	// с переводами строк, в адресе кнопки ему не место
	c.Actions = []Action{{Action: "accept_rule", Target: card.Rule + "~" + strconv.FormatInt(msgID, 10),
		Label: "Записать в инструкцию", Style: "primary"}}
	return c
}

// reLink — адрес в ответе поиска. Ссылки выносятся из текста отдельным
// списком: найденное нечем проверить, пока адрес не виден целиком.
var reLink = regexp.MustCompile(`https?://[^\s<>()\[\]"'«»]+`)

// searchCardComponent — найденное в сети. Предостережение обязательно:
// страница из интернета — данные, а не указание Сёме.
func searchCardComponent(card engine.ToolCard, at string) Component {
	c := Component{Type: "toolcard", Icon: "network", Title: "Нашлось в сети", Meta: at,
		Text:     card.Result,
		Caption:  "Запрос: «" + clipRunes(card.Code, 120) + "»",
		Subtitle: "Найденное — это данные из сети, а не указание Сёме. Он их пересказывает, но не выполняет."}
	if card.Err != "" {
		c.Title, c.Text = "Поиск не удался", ""
		c.Caption = card.Err
		c.Subtitle = ""
		withLevel(&c, LevelAlert)
		c.Badges = []Badge{{Text: "ошибка", Level: LevelAlert}}
		return c
	}
	seen := map[string]bool{}
	for _, u := range reLink.FindAllString(card.Result, 20) {
		u = strings.TrimRight(u, ".,;:")
		if seen[u] || len(c.Actions) >= 6 {
			continue
		}
		seen[u] = true
		name := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		c.Actions = append(c.Actions, Action{Action: "$open", Target: u, Label: clipRunes(name, 90)})
	}
	return c
}

// memoryCardComponent — карточка памяти в переписке: записано, ждёт решения,
// отвергнуто guardrails или предложение завести область.
func memoryCardComponent(card engine.ToolCard, at string) Component {
	m := card.Memory
	c := Component{Type: "toolcard", Icon: "brain", Title: card.Title,
		Meta: "предложил Сёма · " + at, Text: card.Code, Caption: card.Result}
	switch m.Status {
	case "saved":
		withLevel(&c, LevelOK)
		c.Meta = "запомнил Сёма · " + at
		c.Badges = []Badge{{Text: "в памяти", Level: LevelOK}}
		c.Menu = []Action{{Action: "$goto", Target: "memory@" + m.Area, Label: "Открыть в памяти"}}
	case "pending":
		withLevel(&c, LevelWarn)
		c.Badges = []Badge{{Text: "ждёт вас", Level: LevelWarn}}
		id := strconv.FormatInt(m.ID, 10)
		c.Actions = []Action{
			{Action: "accept_memory", Target: id, Label: "Запомнить", Style: "primary"},
			{Action: "accept_memory_area", Target: id, Label: "В другую область", Prompt: "Имя области", Value: m.Area},
			{Action: "forget_memory", Target: id, Label: "Не надо", Style: "destructive"},
		}
	case "rejected":
		withLevel(&c, LevelAlert)
		c.Badges = []Badge{{Text: "не сохранено", Level: LevelAlert}}
		// почему не сохранилось — предостережением, а не тихой строкой:
		// это единственное, ради чего карточку читают
		c.Subtitle, c.Caption = card.Result, ""
	case "suggest":
		withLevel(&c, LevelInfo)
		c.Badges = []Badge{{Text: "предложение", Level: LevelInfo}}
		c.Actions = []Action{
			{Action: "create_area", Target: m.Area, Label: "Завести область", Style: "primary"},
			{Action: "drop_area_suggestion", Target: m.Area, Label: "Не надо", Style: "destructive"},
		}
	}
	return c
}

// sshCardComponent — команда на сервере: выполнена с выводом, ждёт кнопки,
// отказана или отклонена.
func (s *Server) sshCardComponent(card engine.ToolCard, at string) Component {
	c := Component{Type: "toolcard", Icon: "terminal",
		Title: "Команда на «" + card.SSH.Host + "»", Meta: at,
		Detail: card.SSH.Cmd}
	switch card.SSH.Status {
	case "executed":
		c.Title = "Команда выполнена"
		level := LevelOK
		if card.SSH.Code != 0 {
			level = LevelWarn
		}
		withLevel(&c, level)
		c.Badges = []Badge{{Text: fmt.Sprintf("код %d · %s с", card.SSH.Code, secondsText(card.Seconds, 1)), Level: level}}
		if вывод := cleanCR(card.Code); strings.TrimSpace(вывод) != "" {
			c.Detail = card.SSH.Cmd + "\n\n" + вывод
		}
		c.Caption = "Ключ взяло ядро — запись в журнале сервера и в журнале ключа. Секреты в выводе замаскированы."
	case "pending":
		// решение могло прийти позже (из уведомления или с «Серверов») —
		// смотрим состояние сейчас, а не то, что записали при показе
		if decided, found := s.eng.Store().SSHPendingState(card.SSH.PendingID); !found || decided != "" {
			text := "уже решено"
			switch decided {
			case store.SSHExecuted:
				text = "выполнено"
				withLevel(&c, LevelOK)
			case store.SSHRejected:
				text = "отклонено"
			}
			c.Badges = []Badge{{Text: text}}
			c.Caption = "Команда меняет сервер: " + card.SSH.Why
			return c
		}
		withLevel(&c, LevelWarn)
		c.Badges = []Badge{{Text: "ждёт вас", Level: LevelWarn}}
		c.Caption = "Команда меняет сервер: " + card.SSH.Why + ". До нажатия на сервер ничего не уходит."
		id := strconv.FormatInt(card.SSH.PendingID, 10)
		c.Actions = []Action{{Action: "approve_ssh", Target: id, Label: "Выполнить", Style: "primary"}}
		if s.eng.Decide(card.SSH.Host, card.SSH.Cmd).CanAllow() {
			// правило «Разрешить» этому серверу: впредь ровно эта команда пойдёт
			// без вопроса. sudo и неясный синтаксис разрешить нельзя — кнопки нет
			c.Actions = append(c.Actions, Action{Action: "approve_ssh_read", Target: id, Label: "Выполнить и разрешить впредь"})
		}
		c.Actions = append(c.Actions, Action{Action: "reject_ssh", Target: id, Label: "Отклонить", Style: "destructive"})
	case "norule":
		// у сервера нет правила для этой команды: она запрещена по умолчанию, а
		// человек решает, завести ли его. Состояние смотрим сейчас, а не то,
		// что записали при показе: решение могло прийти из «Уведомлений»
		c.Title = "Нет правила для команды"
		n, found := s.eng.Store().SSHNoRuleByID(card.SSH.NoRuleID)
		if !found || n.Decided != "" {
			text := "уже решено"
			if found {
				switch n.Decided {
				case store.NoRuleAsk:
					text = "заведено «Спросить»"
				case store.NoRuleAllow:
					text = "заведено «Разрешить»"
				case store.NoRuleDismissed:
					text = "оставлена запрещённой"
				}
			}
			c.Badges = []Badge{{Text: text}}
			c.Caption = "На сервере «" + card.SSH.Host + "» не было правила для этой команды."
			return c
		}
		withLevel(&c, LevelWarn)
		c.Badges = []Badge{{Text: "нет правила", Level: LevelWarn}}
		c.Caption = "На сервере «" + card.SSH.Host + "» нет правила для этой команды — она не выполнена. " + card.SSH.Why +
			" «Спрашивать для неё» заведёт правило «Спросить», и команда станет обычным подтверждением."
		id := strconv.FormatInt(card.SSH.NoRuleID, 10)
		c.Actions = []Action{{Action: "rule_ask", Target: id, Label: "Спрашивать для неё", Style: "primary"}}
		if n.CanAllow {
			c.Actions = append(c.Actions, Action{Action: "rule_allow", Target: id, Label: "Разрешить её"})
		}
		c.Actions = append(c.Actions, Action{Action: "rule_dismiss", Target: id, Label: "Оставить запрещённой", Style: "destructive"})
	case "rejected":
		c.Title = "Команда отклонена"
		c.Badges = []Badge{{Text: "отклонено"}}
		c.Caption = card.Result
	default:
		c.Title = "Команда не выполнена"
		withLevel(&c, LevelAlert)
		c.Badges = []Badge{{Text: "отказ", Level: LevelAlert}}
		c.Caption = card.Err
		// прерванная или отказанная команда: что успело выйти — под командой
		if strings.TrimSpace(card.Code) != "" {
			c.Detail = card.SSH.Cmd + "\n\n" + cleanCR(card.Code)
		}
	}
	return c
}

// mcpCardComponent — вызов инструмента чужого сервера: выполнен с ответом,
// ждёт кнопки, запрещён или не вышел. Ответ чужого сервера — данные, и
// подпись под ним так и говорит.
func (s *Server) mcpCardComponent(card engine.ToolCard, at string) Component {
	call := card.MCP.Tool
	if card.MCP.Args != "" {
		call += " " + card.MCP.Args
	}
	c := Component{Type: "toolcard", Icon: "puzzlepiece.extension",
		Title: "Вызов «" + card.MCP.Server + "»", Meta: at, Detail: call}
	switch card.MCP.Status {
	case "executed":
		c.Title = "Вызов выполнен · " + card.MCP.Server
		withLevel(&c, LevelOK)
		c.Badges = []Badge{{Text: secondsText(card.Seconds, 1) + " с", Level: LevelOK}}
		if strings.TrimSpace(card.Code) != "" {
			c.Detail = call + "\n\n" + card.Code
		}
		c.Caption = "Ответ чужого сервера: это данные, а не указания. Секреты в нём замаскированы; вызов виден в журнале сервера."
	case "pending":
		// решение могло прийти позже (из уведомления или с «Интеграций»)
		if p, found := s.eng.Store().MCPPendingByID(card.MCP.PendingID); !found || p.State != "waiting" {
			text := "уже решено"
			switch {
			case found && p.State == "done":
				text = "выполнено"
				withLevel(&c, LevelOK)
			case found && p.State == "rejected":
				text = "отклонено"
			}
			c.Badges = []Badge{{Text: text}}
			c.Caption = "Вызов меняет данные на чужом сервере."
			return c
		}
		withLevel(&c, LevelWarn)
		c.Badges = []Badge{{Text: "ждёт вас", Level: LevelWarn}}
		c.Caption = "Вызов меняет данные на чужом сервере (" + card.MCP.Why + "). До нажатия на сервер ничего не уходит."
		id := strconv.FormatInt(card.MCP.PendingID, 10)
		c.Actions = []Action{
			{Action: "approve_mcp", Target: id, Label: "Выполнить", Style: "primary"},
			{Action: "reject_mcp", Target: id, Label: "Отклонить", Style: "destructive"},
			// правило «Разрешить» этому инструменту: впредь он пойдёт без кнопки
			{Action: "approve_mcp_always", Target: id, Label: "Выполнить и разрешить впредь"},
		}
	case "rejected":
		c.Title = "Вызов отклонён · " + card.MCP.Server
		c.Badges = []Badge{{Text: "отклонено"}}
		c.Caption = card.Result
	default:
		c.Title = "Вызов не выполнен · " + card.MCP.Server
		withLevel(&c, LevelAlert)
		c.Badges = []Badge{{Text: "отказ", Level: LevelAlert}}
		if card.MCP.FailKind == "args" {
			c.Badges = []Badge{{Text: "формат аргументов", Level: LevelAlert}}
		}
		// суть ошибки — первая строка текста (её ставит ядро), она и стоит в
		// подписи; сырой ответ сервера под ней уходит в раскрывающуюся часть
		head, raw, _ := strings.Cut(card.Err, "\n")
		c.Caption = head
		if raw = strings.TrimSpace(raw); raw != "" {
			c.Detail = call + "\n\n" + raw
		}
	}
	return c
}

// reChatFileRef — «(#271)» в сообщении о вложенном файле.
var reChatFileRef = regexp.MustCompile(`\(#(\d{1,9})\)`)

// humanResult — итог задачи человеческим языком. Скрипт возвращает объект,
// и в строке задачи стоял сериализованный JSON целиком: «{"title":"Письма
// за сутки","code":"const since = Date.now() …».
func humanResult(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		return raw
	}
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return raw
	}
	for _, key := range []string{"result", "text", "summary", "answer", "title"} {
		if v, ok := obj[key]; ok {
			if str, ok := v.(string); ok && strings.TrimSpace(str) != "" {
				return str
			}
		}
	}
	return "готово"
}

// tgCardComponent — предложенное сообщение в Telegram. Отправляет человек
// кнопкой: сообщение от его имени необратимо, а «написал не тому» назад
// не вернёшь.
func tgCardComponent(card engine.ToolCard, at string) Component {
	c := Component{Type: "toolcard", Icon: "paperplane",
		Title: "Ответ в Telegram", Meta: card.TG.Who + " · " + at,
		Text: card.TG.Text}
	switch card.TG.Status {
	case "sent":
		withLevel(&c, LevelOK)
		c.Title = "Ушло в Telegram"
		c.Badges = []Badge{{Text: "отправлено", Level: LevelOK}}
	case "denied":
		withLevel(&c, LevelAlert)
		c.Title = "Написать не вышло"
		c.Badges = []Badge{{Text: "нельзя", Level: LevelAlert}}
		c.Caption = card.TG.Why
	case "rejected":
		c.Title = "Черновик отклонён"
		c.Badges = []Badge{{Text: "отклонено"}}
	default:
		withLevel(&c, LevelWarn)
		c.Badges = []Badge{{Text: "ждёт вас", Level: LevelWarn}}
		c.Caption = "Сёма предложил текст — собеседник его пока не видит."
		c.Actions = []Action{
			// цель — номер сообщения в исходящих, а не чата: отправляется
			// ровно то, что предложил Сёма
			{Action: "tg_send", Target: strconv.FormatInt(card.TG.ID, 10), Label: "Отправить", Style: "primary",
				Confirm: "Отправить сообщение «" + card.TG.Who + "»?\n\n" + card.TG.Text, ConfirmOK: "Отправить"},
			{Action: "tg_edit", Target: strconv.FormatInt(card.TG.ID, 10), Label: "Поправить",
				Prompt: "Текст сообщения", Value: card.TG.Text},
			{Action: "tg_reject", Target: strconv.FormatInt(card.TG.ID, 10), Label: "Не отправлять", Style: "destructive"},
		}
	}
	return c
}

// chatTitleLen — сколько знаков имени чата помещается в панель. Обрезка
// идёт по словам: «Сервер srv-vpn работает стабильно, о» читать невозможно.
const chatTitleLen = 34

// clipWords — обрезка по границе слова, а не по знаку.
func clipWords(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	cut := string(r[:limit])
	if i := strings.LastIndexAny(cut, " ·,;:"); i > limit/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ·,;:") + "…"
}

// chatPreview — последняя реплика человеческим языком. Карточка в
// переписке хранится как JSON, и в списке от неё оставалась каша из
// фигурных скобок вместо «Сёма: выполнила команду».
func chatPreview(last, group string) string {
	last = strings.TrimSpace(strings.ReplaceAll(last, "\n", " "))
	if last == "" {
		return "пусто"
	}
	if card, ok := engine.ParseToolCard(last); ok {
		title := strings.TrimSpace(card.Title)
		if title == "" {
			title = "карточка"
		}
		return "Сёма: " + clipWords(title, 60)
	}
	return clipWords(last, 60)
}

// whenShort — время последнего сообщения так, как его читают в списке:
// сегодняшнее часами, вчерашнее словом, старое датой.
func whenShort(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	now := time.Now()
	switch {
	case at.YearDay() == now.YearDay() && at.Year() == now.Year():
		return at.Format("15:04")
	case now.Sub(at) < 48*time.Hour:
		return "вчера"
	case at.Year() == now.Year():
		return at.Format("02.01")
	}
	return at.Format("01.2006")
}

// chatPage — сколько сообщений отдаётся за раз. Настройка, а не число в
// коде: у одного чата хватает двадцати, в разборе сервера нужно больше.
func (s *Server) chatPage() int {
	n, err := strconv.Atoi(strings.TrimSpace(s.eng.Store().Setting("chats.page", "50")))
	if err != nil || n < 5 {
		return 50
	}
	return n
}

// dayLine — подпись разделителя дня.
func dayLine(at time.Time) string {
	now := time.Now()
	switch {
	case at.YearDay() == now.YearDay() && at.Year() == now.Year():
		return "сегодня"
	case now.Sub(at) < 48*time.Hour:
		return "вчера"
	}
	return ruDateShort(at)
}

// ruDateShort — «5 сентября», без года для этого года: месяцы по-русски
// стандартная библиотека не умеет.
func ruDateShort(at time.Time) string {
	months := []string{"января", "февраля", "марта", "апреля", "мая", "июня",
		"июля", "августа", "сентября", "октября", "ноября", "декабря"}
	out := fmt.Sprintf("%d %s", at.Day(), months[int(at.Month())-1])
	if at.Year() != time.Now().Year() {
		out += fmt.Sprintf(" %d", at.Year())
	}
	return out
}

// cleanCR — вывод команд с возвратом каретки: «Reading database … 5 % …
// 100 %» — это одна строка, которую перерисовывали, а в переписке она
// расползалась на двадцать.
func cleanCR(out string) string {
	if !strings.Contains(out, "\r") {
		return out
	}
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if j := strings.LastIndex(l, "\r"); j >= 0 {
			lines[i] = l[j+1:]
		}
	}
	return strings.Join(lines, "\n")
}

// threadWaits — в ветке есть то, что ждёт человека: черновик, команда на
// подтверждении, тикающий таймер. Такую ветку не сворачиваем.
func threadWaits(items []Component) bool {
	for _, c := range items {
		if len(c.Actions) > 0 || c.Until != "" {
			return true
		}
		for _, b := range c.Badges {
			if b.Level == LevelWarn || b.Level == LevelAlert {
				return true
			}
		}
	}
	return false
}

// chatWaiting — что в этом чате ждёт решения человека.
func (s *Server) chatWaiting(chatID int64) string {
	st := s.eng.Store()
	cmds := 0
	if list, err := st.SSHPendingList(); err == nil {
		for _, p := range list {
			if p.ChatID == chatID {
				cmds++
			}
		}
	}
	if list, err := st.MCPPendingList(); err == nil {
		for _, p := range list {
			if p.ChatID == chatID {
				cmds++
			}
		}
	}
	drafts := st.CountTGDrafts(chatID)
	switch {
	case cmds > 0 && drafts > 0:
		return fmt.Sprintf("ждёт вашего решения: %s и %s",
			plural(cmds, "команда", "команды", "команд"), plural(drafts, "черновик", "черновика", "черновиков"))
	case cmds > 0:
		return "ждёт вашего решения: " + plural(cmds, "команда", "команды", "команд")
	case drafts > 0:
		return "ждёт отправки: " + plural(drafts, "черновик", "черновика", "черновиков")
	}
	return ""
}

// chatUnread — в чате есть сообщения, которых вы ещё не видели. Отметка
// ставится при открытии чата (`chat.read.<id>`): непрочитанное считается
// по времени, а не по «кто-то что-то написал».
//
// Отметки берутся одним снимком на все чаты (SettingsByPrefixShared): меню
// спрашивает их у каждого чата на каждом экране.
func (s *Server) chatUnread(c *store.Chat) bool {
	if c.LastAt.IsZero() {
		return false
	}
	seen := s.eng.Store().SettingsByPrefixShared("chat.read.")[fmt.Sprintf("chat.read.%d", c.ID)]
	if seen == "" {
		return true
	}
	at, err := time.Parse(time.RFC3339, seen)
	return err != nil || c.LastAt.After(at)
}

// markChatRead — чат открыли: с этого момента он прочитан.
func (s *Server) markChatRead(chatID int64) {
	_ = s.eng.Store().SetSetting(fmt.Sprintf("chat.read.%d", chatID),
		time.Now().Format(time.RFC3339))
}

// chatThreads — треды разговора: ветки Сёмы под ним.
func (s *Server) chatThreads(chatID int64) []*store.Chat {
	kids, err := s.eng.Store().ChatChildren(chatID)
	if err != nil {
		return nil
	}
	var out []*store.Chat
	for _, k := range kids {
		if k.IsThread() {
			out = append(out, k)
		}
	}
	return out
}

// subjectInThread — тема письма в списке цепочки. У переписки тема одна на
// всех, и повторять её одиннадцать раз незачем; показываем, только когда
// она отличается от темы экрана.
func subjectInThread(тема, экранная string) string {
	if strings.EqualFold(strings.TrimSpace(тема), strings.TrimSpace(экранная)) {
		return ""
	}
	return strings.TrimSpace(тема)
}

// subjectOr — тема письма или честная замена: пустой заголовок экрана
// выглядит как сломанная загрузка.
func subjectOr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(без темы)"
	}
	return s
}

// buildCategoriesMoved — категории трат жили в модуле «Чеки», а он снят
// 10 сентября 2026. Экран оставлен, чтобы старая ссылка вела к правде, а
// не открывала «Почту»: суммы теперь стоят у самих файлов, и смотреть их
// надо там.
func (s *Server) buildCategoriesMoved(doc *Document) error {
	doc.Heading = "Категории трат"
	doc.Layout = "read"
	doc.Subheading = "модуль «Чеки» снят — суммы живут у файлов"
	doc.Components = append(doc.Components, emptyState("Отдельных категорий больше нет",
		"Получателя, сумму и итог Сёма пишет прямо в запись файла, когда файл приходит: чек — это файл с суммой, а не отдельная сущность. Прежние девяносто семь чеков перенесены выпиской в «Файлы» — она ищется как любой другой документ.",
		"rublesign.circle"))
	doc.Components = append(doc.Components, Component{Type: "button", Actions: []Action{
		{Action: "$goto", Target: "files", Label: "Открыть «Файлы»", Style: "primary"}}})
	return nil
}

// messageUnsubscribe — как отписаться от рассылки, если она сама об этом
// сказала. Заголовок List-Unsubscribe несёт один или два адреса: ссылку и
// почтовый адрес. Ссылку показываем ссылкой — переход делает человек;
// письмо за него Сёма не отправляет, потому что отправлять почту она
// вообще не умеет, и делать вид, что умеет, нельзя.
func messageUnsubscribe(m *store.Message) *Component {
	ссылка, почта := parseUnsubscribe(m.ListUnsub)
	if ссылка == "" && почта == "" {
		return nil
	}
	r := Component{Type: "row", Layout: "wide", Icon: "envelope.badge",
		Subtitle: "Отписаться от рассылки",
		Caption:  "адрес отписки указал сам отправитель в заголовке письма"}
	if ссылка != "" {
		r.Actions = append(r.Actions, Action{Action: "$open", Target: ссылка, Label: "Открыть страницу отписки"})
	}
	if почта != "" {
		r.Props = append(r.Props, Prop{Label: "Письмом на", Value: почта})
	}
	return &Component{Type: "section", Title: "Рассылка", Children: []Component{r}}
}

// parseUnsubscribe — «<https://…>, <mailto:…>» на две части. Порядок в
// заголовке произвольный, а http и mailto ведут себя по-разному.
func parseUnsubscribe(raw string) (ссылка, почта string) {
	for _, часть := range strings.Split(raw, ",") {
		часть = strings.TrimSpace(strings.Trim(strings.TrimSpace(часть), "<>"))
		switch {
		case strings.HasPrefix(часть, "https://"), strings.HasPrefix(часть, "http://"):
			if ссылка == "" {
				ссылка = часть
			}
		case strings.HasPrefix(часть, "mailto:"):
			if почта == "" {
				почта = strings.TrimPrefix(часть, "mailto:")
			}
		}
	}
	return ссылка, почта
}

// messageReminder — что написано в строке: уже отложено или ещё нет.
// Молчаливая строка «Напомнить» не говорит, стоит ли напоминание, и
// человек ставит его второй раз.
func messageReminder(s *Server, id int64) string {
	n, ok := s.eng.Store().OpenNotificationByKey(snoozeKey(id))
	if !ok || n.RemindAt == "" {
		return "Напомнить об этом письме"
	}
	t, err := time.Parse(time.RFC3339, n.RemindAt)
	if err != nil {
		return "Напомнить об этом письме"
	}
	return "Напоминание стоит на " + t.Local().Format("02.01 15:04")
}

func snoozeKey(id int64) string { return "msg-later:" + strconv.FormatInt(id, 10) }

// messageReply — форма ответа. Надпись на кнопке говорит, что случится:
// «В черновики» кладёт письмо в ящик, «Отправить» отправляет. Человек
// должен понимать это до нажатия, а не после.
func (s *Server) messageReply(m *store.Message) Component {
	id := strconv.FormatInt(m.ID, 10)
	режим := s.eng.SendMode()
	кнопка, пояснение := "В черновики", "ответ ляжет в папку «Черновики» вашего ящика — отправите вы сами"
	if режим != engine.ModeDraft {
		кнопка = "Отправить"
		пояснение = "письмо уйдёт сразу и отозвать его будет нельзя"
		if готово, почему := s.eng.SendReady(m.Account); !готово {
			кнопка = "В черновики"
			пояснение = "отправка не настроена (" + почему + ") — ответ ляжет в черновики"
		}
	}
	кому := "некому: у письма нет обратного адреса"
	if а := replyRecipient(m); а != "" {
		кому = а
	}
	// что случилось с прошлым ответом — здесь же: иначе человек нажал
	// кнопку и не знает, ушло письмо или легло в черновики
	s.mu.Lock()
	итог := s.lastReply
	s.lastReply = ""
	s.mu.Unlock()
	if итог != "" {
		пояснение = итог + " · " + пояснение
	}
	return Component{Type: "form", ID: "ответ-" + id, Title: "Ответить",
		Fields: []Field{
			{Type: "text", Name: "кому", Label: "Кому", Value: кому,
				Hint: "адрес берётся из письма: Reply-To весомее From — рассылки нарочно ставят его, чтобы ответ попал живому человеку"},
			{Type: "code", Name: "текст", Label: "Текст ответа",
				Hint: пояснение},
		},
		Submit: &Action{Action: "reply_msg", Target: id, Label: кнопка, Style: "primary"}}
}

// replyRecipient — кому уйдёт ответ, для показа человеку. Считает ровно то
// же, что и ядро при отправке: две разные правды здесь были бы обманом.
func replyRecipient(m *store.Message) string {
	if a := strings.TrimSpace(m.ReplyTo); a != "" {
		return a
	}
	return strings.TrimSpace(m.FromAddr)
}

// waitingInFeed — открытые записи уведомлений внизу разговора с Сёмой.
// Пункта «Ждут вас» в меню больше нет: всё, что ждёт человека, приходит
// туда, где он отвечает. Спокойные записи (ok) сворачиваются в одну
// строку — четыре ночные смены не должны занимать четыре карточки.
func (s *Server) waitingInFeed(doc *Document, chat *store.Chat) {
	if chat == nil || !s.isMainConversation(chat.ID) {
		return
	}
	list, err := s.eng.Store().Notifications(true, 100)
	if err != nil || len(list) == 0 {
		return
	}
	var спокойных int
	var последняя *store.Notification
	for _, n := range list {
		if n.Level == "ok" {
			спокойных++
			if последняя == nil {
				последняя = n
			}
			continue
		}
		doc.Components = append(doc.Components, s.notifyRow(n, false))
	}
	if спокойных == 0 {
		return
	}
	итог := последняя.Title
	if спокойных > 1 {
		итог = "Сёма сделал сам · " + strconv.Itoa(спокойных)
	}
	doc.Components = append(doc.Components, Component{
		Type: "section", ID: "спокойные-записи", Collapsed: true,
		Title: итог, Subtitle: "решать нечего", Children: s.calmRows(list),
	})
}

func (s *Server) calmRows(list []*store.Notification) []Component {
	var rows []Component
	for _, n := range list {
		if n.Level == "ok" {
			rows = append(rows, s.notifyRow(n, false))
		}
	}
	return rows
}

// unreadConversations — сколько разговоров ждут прочтения. Это и
// значит счётчик рядом со словом «Чаты»: цифра в списке переписки
// читается как непрочитанное, а не как «столько дел».
// Список чатов приходит готовым: меню читает его один раз на оба пункта
// (счётчик и дерево) — это самый тяжёлый запрос меню.
func (s *Server) unreadConversations(chats []*store.Chat) string {
	n := 0
	for _, c := range chats {
		if !c.IsContainer() && s.chatUnread(c) {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// nameInGroup — название разговора без приставки канала: группа уже
// называет его сама, «Telegram · » перед каждой строкой только съедает
// место в узком меню.
func nameInGroup(title string) string {
	return strings.TrimPrefix(strings.TrimPrefix(title, "Telegram · "), "MAX · ")
}

// chatScreens — разговоры в меню деревом: разговор с Сёмой первым, дальше
// группы (Telegram, MAX, задачи…), в них разговоры по свежести, у группы с
// темами — её темы. Раньше это дерево жило второй панелью рядом с меню и
// дублировало его. Группы раскрыты всегда: раскрытие «где стоишь» двигало
// меню при каждом переходе между группами.
// notifyLvl — уровень уведомлений, уже посчитанный меню. Список chats — общий
// с соседями по меню: его элементы здесь не правятся.
func (s *Server) chatScreens(screen string, chats []*store.Chat, notifyLvl string) []Screen {
	st := s.eng.Store()
	открыт, _ := strconv.ParseInt(strings.TrimPrefix(screen, "chat:"), 10, 64)
	kids := map[int64][]*store.Chat{}
	var roots []*store.Chat
	for _, c := range chats {
		if c.Parent > 0 {
			kids[c.Parent] = append(kids[c.Parent], c)
			continue
		}
		roots = append(roots, c)
	}
	черновики := st.TGDraftCounts()
	пункт := func(c *store.Chat, g string) Screen {
		значок, оттенок := conversationIcon(c, g)
		п := Screen{ID: "chat:" + strconv.FormatInt(c.ID, 10), Title: nameInGroup(c.Title), Icon: значок, Tint: оттенок}
		switch {
		case черновики[c.ID] > 0:
			п.Level = LevelAlert // черновик не уйдёт без кнопки
		case s.chatUnread(c):
			п.Level = LevelInfo // новое, ещё не открытое
		}
		return п
	}

	var out []Screen
	// разговор с Сёмой — первым: сюда приходит всё, что ждёт решения
	главный := mainConversation(chats)
	for _, c := range chats {
		if c.ID == главный {
			out = append(out, Screen{ID: "chat:" + strconv.FormatInt(c.ID, 10), Title: c.Title, Level: notifyLvl,
				Initials: initials(c.Title), Tint: "sky"})
		}
	}

	byGroup := map[string][]*store.Chat{}
	// у контейнера своих сообщений нет: его место задаёт свежий разговор под
	// ним. Свежесть — своей картой: список общий с соседями по меню, и
	// правка LastAt на месте досталась бы им (ревью perf-pass 3)
	свежесть := map[int64]time.Time{}
	last := func(c *store.Chat) time.Time {
		if t, ok := свежесть[c.ID]; ok {
			return t
		}
		return c.LastAt
	}
	for _, c := range roots {
		if c.IsContainer() {
			t := c.LastAt
			for _, k := range kids[c.ID] {
				if k.LastAt.After(t) {
					t = k.LastAt
				}
			}
			свежесть[c.ID] = t
		}
		g := chatGroup(c.Title)
		byGroup[g] = append(byGroup[g], c)
	}
	for _, g := range chatChips {
		list := byGroup[g]
		if len(list) == 0 {
			continue
		}
		sort.SliceStable(list, func(i, j int) bool { return last(list[i]).After(last(list[j])) })
		var children []Screen
		for _, c := range list {
			if c.IsContainer() {
				var темы []Screen
				ks := kids[c.ID]
				sort.SliceStable(ks, func(i, j int) bool { return ks[i].LastAt.After(ks[j].LastAt) })
				for _, k := range ks {
					if k.IsThread() {
						continue
					}
					темы = append(темы, пункт(k, g))
					// открытая ветка темы — рядом с темой
					for _, в := range kids[k.ID] {
						if в.IsThread() && в.ID == открыт && в.ID != главный {
							ветка := пункт(в, g)
							ветка.Title = "↳ " + в.Title
							темы = append(темы, ветка)
						}
					}
				}
				узел := пункт(c, g)
				узел.Children, узел.Open, узел.NoSelf = темы, true, true
				children = append(children, узел)
				continue
			}
			children = append(children, пункт(c, g))
			// открытая ветка разговора — рядом с ним, иначе в меню нечего
			// подсветить. Разговор с Сёмой уже стоит первым
			for _, k := range kids[c.ID] {
				if k.IsThread() && k.ID == открыт && k.ID != главный {
					ветка := пункт(k, g)
					ветка.Title = "↳ " + k.Title
					children = append(children, ветка)
				}
			}
		}
		значок, оттенок := chatGroupIcon(g)
		out = append(out, Screen{ID: "чаты:" + g, Title: g, Open: true, NoSelf: true, Children: children,
			Icon: значок, Tint: оттенок, Tile: true})
	}
	return out
}

// mainConversation — разговор с Сёмой, куда приходит всё, что ждёт
// решения. Ветка вида «Сёма» заводится под каждым разговором — у
// переписки с человеком своя, у группы своя, — поэтому «первая попавшаяся
// ветка Сёмы» означала случайную, а записи уведомлений появлялись бы в
// каждой. Главная — самая старая: она не меняется от новых разговоров.
func mainConversation(chats []*store.Chat) int64 {
	var id int64
	for _, c := range chats {
		if c.Kind == store.ChatKindSema && (id == 0 || c.ID < id) {
			id = c.ID
		}
	}
	return id
}

// isMainConversation — открыт ли главный разговор с Сёмой.
func (s *Server) isMainConversation(chatID int64) bool {
	chats, err := s.eng.Store().Chats()
	return err == nil && chatID > 0 && mainConversation(chats) == chatID
}

// chatGroupIcon — плитка группы разговоров: мессенджер узнаётся по
// своему цвету, остальные группы — по смыслу.
func chatGroupIcon(g string) (значок, оттенок string) {
	switch g {
	case "Telegram":
		return "logo.telegram", "sky"
	case "MAX":
		return "logo.max", "violet"
	case "Задачи":
		return "checkmark", "green"
	case "Разбор уведомлений":
		return "bell", "orange"
	}
	return "bubble.left", "blue"
}

// conversationIcon — значок разговора по его виду. Аватаров собеседников у
// Сёмы нет: фото из мессенджеров он не скачивает, поэтому значок говорит,
// что это за разговор, — человек, группа, тема, канал, задача, — а не
// выдумывает картинку по имени.
func conversationIcon(c *store.Chat, g string) (значок, оттенок string) {
	switch {
	case c.Kind == store.ChatKindTopic:
		return "number", "grey"
	case c.Kind == store.ChatKindChannel:
		return "megaphone", "grey"
	case c.Kind == store.ChatKindPost:
		return "doc", "grey"
	case c.IsThread():
		return "bubble.left", "grey"
	case c.Kind == store.ChatKindGroup:
		return "person.2", "blue"
	case g == "Задачи":
		return "list.bullet", "grey"
	case g == "Разбор уведомлений":
		return "bell", "grey"
	case g == "Мои чаты":
		return "bubble.left", "grey"
	}
	return "person", "blue"
}

// initials — первые буквы двух первых слов названия: «Сёма · Вадим» →
// «СВ». Разделители вроде «·» словами не считаются.
func initials(title string) string {
	var out []rune
	for _, слово := range strings.Fields(title) {
		for _, r := range слово {
			if unicode.IsLetter(r) {
				out = append(out, unicode.ToUpper(r))
			}
			break
		}
		if len(out) == 2 {
			break
		}
	}
	return string(out)
}
