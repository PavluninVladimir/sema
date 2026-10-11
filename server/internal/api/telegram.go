package api

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Вкладка «Telegram»: ключи приложения, вход по телефону и коду, выбор
// чатов. Ничего не собирается, пока человек не включил чат.

const tgScreen = "settings:telegram"

// Внутренние вкладки Telegram. На одной ленте лежали ключи приложения,
// вход по телефону, четырнадцать собеседников с их правами и топиками,
// пределы обращений и пределы переписки — 1487 узлов. Собеседник, ответ
// и подключение — три разных дела, и приходят сюда ровно с одним.
const (
	tgTabChats  = ""       // Собеседники — кому что разрешено
	tgTabAnswer = "answer" // Ответы и пределы
	tgTabConn   = "conn"   // Подключение: ключи, вход, заходы
)

// tgTarget — адрес чата в действиях: учётка и номер. Номер чата сам по
// себе ничей — один и тот же id встречается в личной и в рабочей.
func tgTarget(account string, id int64) string {
	return engine.NumberDigits(account) + "." + strconv.FormatInt(id, 10)
}

// tgFromTarget — обратное: учётка и номер чата из адреса действия.
func (s *Server) tgFromTarget(target string) (*store.TGAccount, int64, error) {
	digits, num, ok := strings.Cut(target, ".")
	if !ok {
		return nil, 0, fmt.Errorf("нужны учётка и номер чата")
	}
	acc, has := s.eng.TGAccountByDigits(digits)
	if !has {
		return nil, 0, fmt.Errorf("учётки Telegram %s нет", digits)
	}
	id, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return nil, 0, fmt.Errorf("нужен номер чата")
	}
	return acc, id, nil
}

// tgBase — адрес экрана выбранной учётки: «settings:telegram@79990000000».
// Плюс в адрес не кладём — в строке запроса он значит пробел.
func tgBase(acc *store.TGAccount) string {
	if acc == nil {
		return tgScreen
	}
	return tgScreen + "@" + engine.NumberDigits(acc.Phone)
}

// accPhone — номер выбранной учётки; пусто, когда учёток нет вовсе.
func accPhone(a *store.TGAccount) string {
	if a == nil {
		return ""
	}
	return a.Phone
}

func (s *Server) buildTelegram(doc *Document, addr, tab, filter, search string, limit int) error {
	doc.Heading = "Telegram"
	doc.Subheading = "Переписка от вашего имени: какие чаты, темы и каналы Сёма читает и где отвечает"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	st := s.eng.Store()

	учётки := s.eng.TGAccounts()
	// забытую учётку в закладке открывать нечем — показываем первую, а не
	// пустой экран
	acc, естьУчётка := s.eng.TGAccountOrFirst(addr)
	_, credsErr := s.eng.TGCreds()

	// вкладки появляются, когда хоть одна учётка вошла: мастер входа —
	// один путь из трёх шагов, и резать его на части значит прятать
	// следующий шаг. Вторая учётка заводится уже при живых вкладках —
	// и пока она ждёт код, вкладки пропадать не должны
	естьВход := false
	for _, a := range учётки {
		if a.State == "ok" {
			естьВход = true
		}
	}
	if credsErr != nil || !естьВход {
		tab = tgTabConn
	} else {
		chats, _ := st.TGChats(accPhone(acc), false)
		setSubtabs(doc, tgBase(acc), tab, []innerTab{
			{ID: tgTabChats, Name: "Собеседники", Count: strconv.Itoa(len(chats))},
			{ID: tgTabAnswer, Name: "Ответы и пределы"},
			{ID: tgTabConn, Name: "Учётки", Count: strconv.Itoa(len(учётки))},
		})
		if tab != tgTabConn {
			if tab == tgTabChats {
				// список читается из Telegram кнопкой; она нужна там, где
				// список смотрят, а не на «Подключении» — «нет кнопки
				// обновить каналы» (13 сентября 2026)
				doc.ToolbarActions = append(doc.ToolbarActions,
					Action{Action: "tg_refresh_chats", Target: engine.NumberDigits(accPhone(acc)), Label: "Обновить список"})
			}
			s.pinnedTGAccount(add, учётки, acc, tab)
			return s.telegramTab(doc, add, acc, tab, filter, search, limit)
		}
	}

	add(Component{Type: "text", Style: "secondary",
		Text: "Учётки Telegram как источник, рядом с ящиками. Бот тут не поможет: он видит только то, что написали ему, " +
			"а нужны ваши чаты. Учёток может быть несколько — личная и рабочая живут рядом, у каждой свои чаты, права и пределы. " +
			"Ключи приложения общие на все учётки: это ключи программы, а не аккаунта. Сессии лежат в хранилище секретов зашифрованными и наружу не выдаются — ни модели, ни модулю."})

	// ключи приложения
	credsRow := Component{Type: "row", Layout: "wide", Level: LevelOK, Dot: levelDot(LevelOK),
		Subtitle: "Ключи приложения", Caption: "api_id и api_hash сохранены в хранилище секретов · общее для всех учёток"}
	if credsErr != nil {
		credsRow.Dot = "#FF9500"
		credsRow.Caption = credsErr.Error()
	}
	add(Component{Type: "section", Title: "Шаг 1 · Ключи приложения", Children: []Component{credsRow}})
	if credsErr != nil {
		add(Component{Type: "form", ID: "tg-creds", Title: "Сохранить ключи",
			Fields: []Field{
				{Type: "text", Name: "app_id", Label: "api_id", Hint: "число с my.telegram.org → API development tools"},
				{Type: "text", Name: "app_hash", Label: "api_hash", Hint: "строка оттуда же"},
			},
			Actions: []Action{{Action: "save_tg_creds", Label: "Сохранить", Style: "primary"}}})
		return nil
	}

	// учётки списком: номер, название, состояние, сколько чатов
	s.tgAccountList(add, учётки, acc)
	if естьУчётка {
		s.tgLoginStep(add, acc)
	}
	// «Добавить аккаунт» — тот же мастер: телефон, код, второй фактор.
	// Пока одна учётка ждёт код, второй вход не предлагаем: код приходит
	// на конкретный номер, и два незаконченных входа путают оба
	if !естьУчётка || acc.State == "ok" {
		add(Component{Type: "form", ID: "tg-phone", Title: "Добавить аккаунт",
			Fields: []Field{
				{Type: "text", Name: "phone", Label: "Телефон", Hint: "с кодом страны: +79991234567"},
				{Type: "text", Name: "title", Label: "Название", Hint: "как вы её зовёте: «личная», «рабочая»"},
			},
			Actions: []Action{{Action: "tg_send_code", Label: "Прислать код", Style: "primary"}}})
	}
	// на «Учётках» это всё: собеседники и пределы переписки живут на
	// своих вкладках, иначе экран снова становится лентой на полторы
	// тысячи узлов
	return nil
}

// tgAccountList — все учётки строками: номер, название, состояние,
// сколько чатов. Отсюда же обновляют список и забывают учётку.
func (s *Server) tgAccountList(add func(Component), учётки []*store.TGAccount, выбрана *store.TGAccount) {
	if len(учётки) == 0 {
		add(Component{Type: "section", Title: "Учётки", Children: []Component{
			emptyState("Ни одной учётки", "Войдите по телефону — код придёт в Telegram на другое устройство", "person.crop.circle"),
		}})
		return
	}
	st := s.eng.Store()
	var rows []Component
	for _, a := range учётки {
		chats, _ := st.TGChats(a.Phone, false)
		читает := 0
		for _, c := range chats {
			if c.Collect {
				читает++
			}
		}
		level, состояние, цвет := LevelOK, "на связи", "#1E7A32"
		switch a.State {
		case "ok":
		case "code":
			level, состояние, цвет = LevelWarn, "ждёт код", "#B25000"
		case "password":
			level, состояние, цвет = LevelWarn, "второй фактор", "#B25000"
		case "fail":
			level, состояние, цвет = LevelAlert, "не подключается", "#C4271C"
		default:
			level, состояние, цвет = "", "не входили", "#8E8E93"
		}
		badges := []Badge{{Text: состояние, Color: цвет}}
		if !a.Enabled {
			badges = append(badges, Badge{Text: "не слушаем", Color: "#8E8E93"})
		}
		// «настраиваем» ничего не говорило: это учётка, чьи чаты и пределы
		// показаны на соседних вкладках
		if выбрана != nil && a.Phone == выбрана.Phone && len(учётки) > 1 {
			badges = append(badges, Badge{Text: "выбрана", Color: "#8E8E93"})
		}
		тумблер := "off"
		if a.Enabled {
			тумблер = "on"
		}
		digits := engine.NumberDigits(a.Phone)
		row := Component{Type: "row", Layout: "wide", ID: "tg-acc-" + digits,
			Avatar: accountAvatar(a), Subtitle: a.Name(), Badges: badges,
			Caption: a.Note, Toggle: тумблер,
			Facts: []Prop{
				{Label: "Чатов", Value: strconv.Itoa(len(chats))},
				{Label: "Читает", Value: strconv.Itoa(читает)},
				{Label: "Заход", Value: "раз в " + strconv.Itoa(a.Every) + " мин, до " + strconv.Itoa(a.Batch) + " сообщений"},
			},
			Actions: []Action{
				{Action: "tg_account_enabled", Target: digits, Label: "toggle"},
				{Action: "$goto", Target: tgBase(a) + "~" + tgTabChats, Label: "Собеседники"},
				{Action: "tg_refresh_chats", Target: digits, Label: "Обновить список"},
				{Action: "tg_account_title", Target: digits, Label: "Переименовать",
					Prompt: "Как называть эту учётку", Value: a.Title},
				{Action: "tg_forget", Target: digits, Label: "Забыть аккаунт", Style: "destructive",
					Confirm:   "Забыть учётку " + a.Name() + "? Сессия сотрётся, собранные сообщения и права чатов останутся.",
					ConfirmOK: "Забыть"},
			}}
		if strings.TrimSpace(a.Title) != "" {
			row.Facts = append([]Prop{{Label: "Номер", Value: a.Phone}}, row.Facts...)
		}
		withLevel(&row, level)
		rows = append(rows, row)
	}
	add(Component{Type: "section", Title: "Учётки · " + strconv.Itoa(len(учётки)), Children: rows})
}

// accountAvatar — буква названия, если учётку назвали. У безымянной
// заголовком стоит номер, и первым знаком выходил «+»: серый кружок с
// плюсом читался как кнопка «добавить» (14 сентября 2026)
func accountAvatar(a *store.TGAccount) string {
	if a == nil || strings.TrimSpace(a.Title) == "" {
		return ""
	}
	return firstRuneOfName(a.Title)
}

// tgLoginStep — следующий шаг мастера для выбранной учётки. Вход идёт по
// одной: код приходит на конкретный номер.
func (s *Server) tgLoginStep(add func(Component), acc *store.TGAccount) {
	digits := engine.NumberDigits(acc.Phone)
	switch acc.State {
	case "code":
		add(Component{Type: "section", Title: "Вход · " + acc.Name(), Children: []Component{{
			Type: "row", Layout: "wide", Level: LevelWarn, Dot: levelDot(LevelWarn), Subtitle: acc.Phone,
			Badges:  []Badge{{Text: "ждёт код", Color: "#B25000"}},
			Caption: acc.Note + "\nсессия ляжет в базу зашифрованной",
			Actions: []Action{{Action: "tg_send_code", Target: acc.Phone, Label: "Прислать снова"},
				{Action: "tg_forget", Target: digits, Label: "Отменить", Style: "destructive"}}}}})
		add(Component{Type: "form", ID: "tg-code", Title: "Код из Telegram для " + acc.Phone,
			Fields:  []Field{{Type: "text", Name: "code", Label: "Код"}},
			Actions: []Action{{Action: "tg_confirm_code", Target: digits, Label: "Войти", Style: "primary"}}})
	case "password":
		add(Component{Type: "section", Title: "Вход · " + acc.Name(), Children: []Component{{
			Type: "row", Layout: "wide", Level: LevelWarn, Dot: levelDot(LevelWarn), Subtitle: acc.Phone,
			Badges: []Badge{{Text: "второй фактор", Color: "#B25000"}}, Caption: acc.Note}}})
		add(Component{Type: "form", ID: "tg-password", Title: "Пароль второго фактора для " + acc.Phone,
			Fields:  []Field{{Type: "text", Name: "password", Label: "Пароль", Hint: "второй фактор Telegram; в базу не пишется"}},
			Actions: []Action{{Action: "tg_confirm_password", Target: digits, Label: "Войти", Style: "primary"}}})
	case "new", "fail":
		row := Component{Type: "row", Layout: "wide", Dot: "#8E8E93", Subtitle: acc.Phone,
			Caption: "Сёма пришлёт код в Telegram на другое устройство",
			Actions: []Action{{Action: "tg_send_code", Target: acc.Phone, Label: "Прислать код", Style: "primary"}}}
		if acc.Note != "" {
			row.Dot, row.Caption = "#C4271C", acc.Note
		}
		add(Component{Type: "section", Title: "Вход · " + acc.Name(), Children: []Component{row}})
	}
}

// pinnedTGAccount — полоса сверху: чей это параметр. Через экран
// прокрутки непонятно, у какой учётки выключаешь собеседника, а список
// рядом позволяет перейти к другой, не возвращаясь на «Учётки».
// Учётка одна — полосы нет: подписывать нечего, экран как прежде.
func (s *Server) pinnedTGAccount(add func(Component), учётки []*store.TGAccount, acc *store.TGAccount, tab string) {
	if acc == nil || len(учётки) < 2 {
		return
	}
	chats, _ := s.eng.Store().TGChats(acc.Phone, false)
	row := Component{Type: "row", Layout: "wide", ID: "учётка-закреплена",
		Avatar: accountAvatar(acc), Subtitle: acc.Name(),
		Caption: acc.Phone + " · " + plural(len(chats), "чат", "чата", "чатов")}
	if !acc.Enabled {
		row.Badges = append(row.Badges, Badge{Text: "не слушаем"})
	}
	names := make([]string, 0, len(учётки))
	for _, a := range учётки {
		names = append(names, a.Name())
	}
	row.Selects = []Select{{Name: "value", Label: "Учётка", Value: acc.Name(), Options: names,
		Action: Action{Action: "goto_tg_account", Target: tab}}}
	add(Component{Type: "section", ID: "закреп", Style: "sticky", Children: []Component{row}})
}

// telegramTab — то, ради чего сюда заходят каждый день: кому что
// разрешено и как Сёма отвечает. Вход, ключи приложения и «забыть
// аккаунт» остались на «Подключении».
func (s *Server) telegramTab(doc *Document, add func(Component), acc *store.TGAccount, tab, filter, search string, limit int) error {
	if acc == nil {
		return nil
	}
	if tab == tgTabAnswer {
		s.tgAnswerTab(add, acc)
		return nil
	}
	return s.tgChatsTab(doc, add, acc, filter, search, limit)
}

// tgSplit — собеседники (люди и группы) отдельно от каналов: у них
// разные права и разные экраны.
func tgSplit(all []*store.TGChat) (chats, channels []*store.TGChat) {
	for _, c := range all {
		if c.Kind == "channel" {
			channels = append(channels, c)
		} else {
			chats = append(chats, c)
		}
	}
	return chats, channels
}

// tgTopicTarget — «учётка.чат/тема» из цели действия.
func (s *Server) tgTopicTarget(target string) (*store.TGAccount, int64, int, error) {
	chatStr, topicStr, ok := strings.Cut(target, "/")
	if !ok {
		return nil, 0, 0, fmt.Errorf("нужны чат и тема")
	}
	acc, id, err := s.tgFromTarget(chatStr)
	if err != nil {
		return nil, 0, 0, err
	}
	topic, err := strconv.Atoi(topicStr)
	if err != nil || topic <= 0 {
		return nil, 0, 0, fmt.Errorf("нужен номер темы")
	}
	return acc, id, topic, nil
}

func (s *Server) telegramActions(req actionRequest) (string, error) {
	ctx := context.Background()
	st := s.eng.Store()
	switch req.Action {
	case "save_tg_creds":
		return tgScreen, s.eng.SaveTGCreds(req.Params["app_id"], req.Params["app_hash"])
	case "goto_tg_account":
		// список в закреплённой полосе: перейти к другой учётке, оставшись
		// на той же внутренней вкладке
		name := strings.TrimSpace(req.Params["value"])
		for _, a := range s.eng.TGAccounts() {
			if a.Name() != name {
				continue
			}
			куда := tgBase(a)
			if req.Target != "" {
				куда += "~" + req.Target
			}
			return куда, nil
		}
		return "", fmt.Errorf("учётки «%s» нет", name)
	case "tg_send_code":
		phone := strings.TrimSpace(req.Params["phone"])
		if phone == "" {
			phone = req.Target
		}
		err := s.eng.TGStartLogin(ctx, phone, req.Params["title"])
		// возвращаемся к той учётке, которой послали код: следующий шаг
		// мастера — её, а не первой в списке
		куда := tgScreen + "~" + tgTabConn
		if acc, ok := s.eng.TGAccountByDigits(phone); ok {
			куда = tgBase(acc) + "~" + tgTabConn
		}
		return куда, err
	case "tg_confirm_code":
		acc, ok := s.eng.TGAccountByDigits(req.Target)
		if !ok {
			return "", fmt.Errorf("учётки нет: сначала попросите код")
		}
		return tgScreen + "~" + tgTabConn, s.eng.TGConfirmCode(ctx, acc.Phone, req.Params["code"])
	case "tg_confirm_password":
		acc, ok := s.eng.TGAccountByDigits(req.Target)
		if !ok {
			return "", fmt.Errorf("учётки нет: сначала попросите код")
		}
		return tgScreen + "~" + tgTabConn, s.eng.TGConfirmPassword(ctx, acc.Phone, req.Params["password"])
	case "tg_account_title":
		acc, ok := s.eng.TGAccountByDigits(req.Target)
		if !ok {
			return "", fmt.Errorf("учётки нет")
		}
		acc.Title = strings.TrimSpace(req.Params["value"])
		return tgScreen + "~" + tgTabConn, st.SaveTGAccount(acc)
	case "tg_account_enabled":
		acc, ok := s.eng.TGAccountByDigits(req.Target)
		if !ok {
			return "", fmt.Errorf("учётки нет")
		}
		// выключенную учётку слушатель отпускает сам на следующем обходе:
		// собранное при этом остаётся, а новые сообщения не приходят
		acc.Enabled = !acc.Enabled
		return tgScreen + "~" + tgTabConn, st.SaveTGAccount(acc)
	case "tg_refresh_chats":
		acc, ok := s.eng.TGAccountByDigits(req.Target)
		if !ok {
			return "", fmt.Errorf("учётки нет: сначала войдите")
		}
		_, err := s.eng.TGRefreshChats(ctx, acc.Phone)
		// возвращаемся туда, где нажали: с «Собеседников» на «Учётки»
		// уводить незачем
		if strings.HasPrefix(req.Screen, tgScreen) {
			return req.Screen, err
		}
		return tgBase(acc), err
	case "tg_send":
		// сообщение уходит от имени человека — только по его кнопке, и
		// текст берётся из исходящих, а не из описания экрана
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", fmt.Errorf("нужен номер сообщения")
		}
		return "", s.eng.ApproveTGSend(ctx, id)
	case "tg_edit":
		// правка черновика — не отправка: текст меняется, кнопка
		// «Отправить» остаётся на месте и ждёт человека, как ждала
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", fmt.Errorf("нужен номер сообщения")
		}
		text := strings.TrimSpace(req.Params["value"])
		if text == "" {
			return "", fmt.Errorf("пустой текст отправлять некому")
		}
		return "", st.EditTGDraft(id, text)
	case "tg_reject":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", fmt.Errorf("нужен номер сообщения")
		}
		return "", st.DecideTGOutbox(id, "rejected", "человек отказался")
	case "tg_chat_right":
		target, right, ok := strings.Cut(req.Target, "~")
		if !ok {
			return "", fmt.Errorf("нужны чат и право")
		}
		acc, id, err := s.tgFromTarget(target)
		if err != nil {
			return "", err
		}
		c, found := st.TGChat(acc.Phone, id)
		if !found {
			return "", fmt.Errorf("чата %d нет в учётке %s", id, acc.Name())
		}
		now := map[string]bool{"collect": c.Collect, "reply": c.Reply, "send": c.Send,
			"files": c.Files, "blocked": c.Blocked, "admin": c.Admin}[right]
		if right == "admin" && !now && c.Kind != "user" {
			return "", fmt.Errorf("админом может быть только личный чат, не группа и не канал")
		}
		if c.Kind == "channel" && !now && right == "reply" {
			return "", fmt.Errorf("«%s» — канал: отвечать в нём некому, комментарии живут в группе обсуждения", c.Title)
		}
		if c.Kind == "channel" && !now && right == "send" && !c.CanPost {
			return "", fmt.Errorf("в канале «%s» у вашего аккаунта нет права публиковать — обновите список, если права выдали недавно", c.Title)
		}
		if err := st.SetTGChatRight(acc.Phone, id, right, !now); err != nil {
			return "", err
		}
		// заблокировали — снимаем всё остальное: иначе право остаётся
		// висеть и однажды сработает
		if right == "blocked" && !now {
			for _, r := range []string{"collect", "reply", "send", "files", "admin"} {
				_ = st.SetTGChatRight(acc.Phone, id, r, false)
			}
		}
		// включили чтение — чат Сёмы заводится сразу, вместе с прошлой
		// перепиской: ждать следующего входящего человек не должен
		if right == "collect" {
			if now {
				s.eng.TGCloseChat(acc.Phone, id) // выключили — сводка в память, чат на месте
			} else {
				s.tgOpen(acc.Phone, id, c.Title)
			}
		}
		return "", nil
	case "tg_chat_class":
		target, class, ok := strings.Cut(req.Target, "~")
		if !ok {
			return "", fmt.Errorf("нужны чат и класс")
		}
		acc, id, err := s.tgFromTarget(target)
		if err != nil {
			return "", err
		}
		if _, found := st.TGChat(acc.Phone, id); !found {
			return "", fmt.Errorf("чата %d нет в учётке %s", id, acc.Name())
		}
		return "", st.SetTGChatClass(acc.Phone, id, class)
	case "tg_channels_all":
		digits, что, ok := strings.Cut(req.Target, ".")
		if !ok {
			return "", fmt.Errorf("нужны учётка и «on» или «off»")
		}
		acc, has := s.eng.TGAccountByDigits(digits)
		if !has {
			return "", fmt.Errorf("учётки нет")
		}
		on := что == "on"
		if !on && что != "off" {
			return "", fmt.Errorf("нужно «on» или «off»")
		}
		changed, err := s.eng.TGSetChannelsCollect(acc.Phone, on)
		if err != nil {
			return "", err
		}
		// разговоры заводятся в фоне: у каждого канала это заход в
		// Telegram за постами, и десяток каналов держал бы кнопку минуту.
		// Пропущенное доберёт слушатель при старте (TGOpenAll)
		go func() {
			for _, c := range changed {
				if on {
					s.tgOpen(c.Account, c.ID, c.Title)
				} else {
					s.eng.TGCloseChat(c.Account, c.ID)
				}
			}
		}()
		return "", nil
	case "tg_topic_right":
		// право одной темы: явное «да»/«нет» поверх права группы
		target, right, ok := strings.Cut(req.Target, "~")
		if !ok {
			return "", fmt.Errorf("нужны тема и право")
		}
		acc, id, topic, err := s.tgTopicTarget(target)
		if err != nil {
			return "", err
		}
		c, found := st.TGChat(acc.Phone, id)
		if !found {
			return "", fmt.Errorf("чата %d нет в учётке %s", id, acc.Name())
		}
		eff := s.eng.TGEffective(c, topic)
		now := map[string]bool{"collect": eff.Collect, "reply": eff.Reply, "send": eff.Send,
			"files": eff.Files, "blocked": eff.Blocked}[right]
		v := 1
		if now {
			v = 0
		}
		if err := s.eng.TGSetTopicRight(acc.Phone, id, topic, right, v); err != nil {
			return "", err
		}
		if right == "blocked" && v == 1 {
			for _, r := range []string{"collect", "reply", "send", "files"} {
				_ = s.eng.TGSetTopicRight(acc.Phone, id, topic, r, 0)
			}
		}
		if right == "collect" && v == 1 {
			s.tgOpen(acc.Phone, id, c.Title) // разговор темы — сразу, с историей
		}
		return "", nil
	case "tg_topic_inherit":
		acc, id, topic, err := s.tgTopicTarget(req.Target)
		if err != nil {
			return "", err
		}
		for _, r := range []string{"collect", "reply", "send", "files", "blocked"} {
			if err := s.eng.TGSetTopicRight(acc.Phone, id, topic, r, store.TopicInherit); err != nil {
				return "", err
			}
		}
		return "", nil
	case "tg_wait_toggle":
		acc, id, err := s.tgFromTarget(req.Target)
		if err != nil {
			return "", err
		}
		// каналу семафор «ждёт ответа» не нужен: посты идут потоком, ответа на
		// них не бывает, и в матрице прав у канала этой клетки нет
		if c, found := st.TGChat(acc.Phone, id); found && c.Kind == "channel" {
			return "", fmt.Errorf("«%s» — канал: ответа на посты не бывает, напоминать о молчании некому", c.Title)
		}
		return "", s.eng.TGWaitOff(acc.Phone, id, !s.eng.TGWaitIsOff(acc.Phone, id))
	case "tg_toggle_chat":
		acc, id, err := s.tgFromTarget(req.Target)
		if err != nil {
			return "", err
		}
		c, ok := st.TGChat(acc.Phone, id)
		if !ok {
			return "", fmt.Errorf("чата нет в списке")
		}
		if err := s.eng.TGSetCollect(acc.Phone, id, !c.Collect); err != nil {
			return "", err
		}
		if c.Collect {
			s.eng.TGCloseChat(acc.Phone, id)
		} else {
			s.tgOpen(acc.Phone, id, c.Title)
		}
		return "", nil
	case "tg_toggle_send":
		acc, ok := s.eng.TGAccountByDigits(req.Target)
		if !ok {
			return "", fmt.Errorf("учётка не подключена")
		}
		acc.CanSend = !acc.CanSend
		return "", st.SaveTGAccount(acc)
	case "tg_set_limit":
		key := strings.TrimSpace(req.Target)
		value := strings.TrimSpace(req.Params["value"])
		allowed := map[string]bool{
			"tg.reply_max_hour": true, "tg.history": true, "tg.file_limit_mb": true, "tg.post_days": true, "tg.chats_max": true,
			"tg.wait_warn_min": true, "tg.wait_alert_min": true, "tg.wait_msgs": true,
			"tg.help_word": true, "tg.stream_ms": true, "tg.stream_first": true,
		}
		if !allowed[key] {
			return "", fmt.Errorf("неизвестный предел %q", key)
		}
		if key != "tg.help_word" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				return "", fmt.Errorf("нужно число от нуля")
			}
			if key == "tg.stream_ms" && n < 100 {
				return "", fmt.Errorf("не меньше 100 мс: чаще Telegram править сообщение не даёт")
			}
			if key == "tg.stream_first" && n < 1 {
				return "", fmt.Errorf("нужен хотя бы один знак")
			}
		} else if value == "" {
			return "", fmt.Errorf("слово-вызов не может быть пустым")
		}
		return tgScreen, st.SetSetting(key, value)
	case "tg_toggle_flag":
		key := strings.TrimSpace(req.Target)
		def, known := tgFlagDefaults[key]
		if !known {
			return "", fmt.Errorf("неизвестный выключатель %q", key)
		}
		v := "0"
		if st.Setting(key, def) == "0" {
			v = "1"
		}
		return tgScreen, st.SetSetting(key, v)
	case "tg_set_stream":
		mode, ok := tgStreamByLabel(strings.TrimSpace(req.Params["value"]))
		if !ok {
			return "", fmt.Errorf("неизвестный вид ответа")
		}
		if mode == "draft" {
			// выбрал черновик заново — значит пробовать заново: прежний отказ
			// Telegram помнился, чтобы не показывать пустоту каждый раз
			for key := range st.SettingsByPrefix(tgDraftOffPrefix) {
				st.DeleteSettingKey(key)
			}
		}
		return tgScreen, st.SetSetting("tg.stream", mode)
	case "tg_draft_retry":
		acc, ok := s.eng.TGAccountByDigits(req.Target)
		if !ok {
			return "", fmt.Errorf("учётка не подключена")
		}
		st.DeleteSettingKey(tgDraftOffPrefix + acc.Phone)
		return tgScreen, nil
	case "save_tg_rate":
		// «Пределы обращений» учётки — поля «Тонкой настройки» вместо вопросов:
		// прежние `tg_set_every` и `tg_set_batch` работают как работали. Сначала
		// проверяем оба, потом пишем: половина формы не сохраняется
		acc, ok := s.eng.TGAccountByDigits(req.Target)
		if !ok {
			return "", fmt.Errorf("учётка не подключена")
		}
		every, batch := acc.Every, acc.Batch
		for _, f := range []struct {
			param, what string
			to          *int
		}{{"every", "как часто заходить", &every}, {"batch", "сколько сообщений за заход", &batch}} {
			raw, has := req.Params[f.param]
			if !has {
				continue
			}
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || n < 1 {
				return "", fmt.Errorf("%s: нужно число от 1", f.what)
			}
			*f.to = n
		}
		acc.Every, acc.Batch = every, batch
		return "", st.SaveTGAccount(acc)
	case "save_tg_limits":
		// пределы переписки и разговора вживую — поля по реестру настроек вместо
		// вопросов `tg_set_limit` (он остался ради скриптов и старых карточек).
		// Пустое слово-вызов отвергается: читалось бы как умолчание, а человек
		// решил бы, что вызова нет
		if v, has := req.Params["help_word"]; has && strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("слово-вызов не может быть пустым")
		}
		return "", s.saveSettings(req,
			settingParam{"help_word", "tg.help_word", "слово-вызов"},
			settingParam{"reply_max_hour", "tg.reply_max_hour", "автоответов в час"},
			settingParam{"wait_warn_min", "tg.wait_warn_min", "минут до напоминания о молчании"},
			settingParam{"wait_alert_min", "tg.wait_alert_min", "минут до тревоги"},
			settingParam{"wait_msgs", "tg.wait_msgs", "сообщений до тревоги"},
			settingParam{"history", "tg.history", "сообщений истории"},
			settingParam{"file_limit_mb", "tg.file_limit_mb", "мегабайт вложения"},
			settingParam{"post_days", "tg.post_days", "дней, пока пост — разговор"},
			settingParam{"chats_max", "tg.chats_max", "диалогов из списка"},
			settingParam{"stream_ms", "tg.stream_ms", "миллисекунд между правками ответа"},
			settingParam{"stream_first", "tg.stream_first", "знаков до первого сообщения"})
	case "tg_set_every", "tg_set_batch":
		acc, ok := s.eng.TGAccountByDigits(req.Target)
		if !ok {
			return "", fmt.Errorf("учётка не подключена")
		}
		n, err := strconv.Atoi(strings.TrimSpace(req.Params["value"]))
		if err != nil || n < 1 {
			return "", fmt.Errorf("нужно число от 1")
		}
		if req.Action == "tg_set_every" {
			acc.Every = n
		} else {
			acc.Batch = n
		}
		return "", st.SaveTGAccount(acc)
	case "tg_forget":
		acc, ok := s.eng.TGAccountByDigits(req.Target)
		if !ok {
			return tgScreen + "~" + tgTabConn, nil
		}
		return tgScreen + "~" + tgTabConn, s.eng.TGForget(acc.Phone)
	}
	return "", errUnhandled
}

// tgOpen — завести чат Сёмы для собеседника, не подвесив форму: человек
// ждёт ответа экрана, а не выборки всей истории.
func (s *Server) tgOpen(account string, id int64, title string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.eng.TGOpenChat(ctx, account, id); err != nil {
		log.Printf("telegram: чат «%s» не открыт: %v", title, err)
	}
}

// Как ответ хозяину доезжает в Telegram: слова для человека и значения
// настройки `tg.stream`. Порядок — порядок в списке.
var tgStreamKinds = []struct{ Value, Label string }{
	{"edit", "По мере написания (сообщение правится)"},
	{"draft", "Живой черновик Telegram"},
	{"off", "Одним куском, когда готово"},
}

// tgFlagDefaults — выключатели живого разговора и их значения по умолчанию.
// Отметка «прочитано» в чатах с автоответом выключена: она сама снимает
// непрочитанное у владельца на телефоне.
var tgFlagDefaults = map[string]string{"tg.read_mark": "1", "tg.typing": "1", "tg.read_mark_reply": "0"}

// tgDraftOffPrefix — ключи «этот аккаунт живой черновик не принимает»; их
// ставит сам Сёма, получив отказ, а здесь они видны и снимаются.
const tgDraftOffPrefix = "tg.draft_off."

func tgStreamByLabel(label string) (string, bool) {
	for _, k := range tgStreamKinds {
		if k.Label == label {
			return k.Value, true
		}
	}
	return "", false
}

// tgLiveSection — как Сёма ведёт себя в разговоре с вами в Telegram:
// «прочитано», «печатает…» и ответ, который пишется на глазах. Общее для
// всех учёток и всех чатов; включённое по умолчанию — потому что без этого
// разговор с Сёмой в Telegram ощущается «в один конец».
func (s *Server) tgLiveSection(acc *store.TGAccount) Component {
	st := s.eng.Store()
	flag := func(key, title, why string) Component {
		state := "on"
		if st.Setting(key, tgFlagDefaults[key]) == "0" {
			state = "off"
		}
		return Component{Type: "row", Layout: "wide", ID: "tg-flag-" + key, Subtitle: title, Caption: why,
			Toggle: state, Actions: []Action{{Action: "tg_toggle_flag", Target: key, Label: "toggle"}}}
	}
	mode := st.Setting("tg.stream", "edit")
	now := tgStreamKinds[0].Label
	options := make([]string, 0, len(tgStreamKinds))
	for _, k := range tgStreamKinds {
		options = append(options, k.Label)
		if k.Value == mode {
			now = k.Label
		}
	}
	stream := Component{Type: "row", Layout: "wide", ID: "tg-stream", Subtitle: "Как приходит ответ",
		Caption: "правки — сообщение отправляется на первых словах и дописывается; черновик — Telegram показывает текст, пока он пишется " +
			"(принимает ли его аккаунт человека, узнаётся первой пробой); одним куском — как раньше",
		Selects: []Select{{Name: "value", Value: now, Options: options, Action: Action{Action: "tg_set_stream", Target: "tg.stream"}}}}
	if reason := st.Setting(tgDraftOffPrefix+acc.Phone, ""); reason != "" {
		stream.Facts = []Prop{{Label: "Черновик", Value: "аккаунт не принимает: " + reason}}
		stream.Actions = []Action{{Action: "tg_draft_retry", Target: engine.NumberDigits(acc.Phone), Label: "Пробовать снова"}}
	}
	return Component{Type: "section", Title: "Разговор с вами вживую · общее для всех учёток", Children: []Component{
		flag("tg.read_mark", "Отмечать прочитанным в вашем разговоре с Сёмой",
			"как только вы написали Сёме в админ-чат, у вас появляются две галочки, ещё до ответа"),
		flag("tg.read_mark_reply", "Отмечать прочитанным в чатах, где Сёма отвечает сам",
			"по умолчанию выключено: отметка «прочитано» снимает непрочитанное и у вас на телефоне, хотя вы сообщение не открывали; "+
				"«печатает» в таких чатах показывается всегда — собеседник видит, что ему отвечают"),
		flag("tg.typing", "Показывать «печатает…»",
			"пока Сёма думает, запускает скрипты и команды, у собеседника горит «печатает»; гаснет, когда ответ ушёл или что-то сломалось"),
		stream,
	}}
}

// tgAnswerTab — «Ответы и пределы»: что Сёма вправе отправлять сам, как он
// ведёт себя в разговоре с вами и где проходят пределы переписки. Значения,
// которые правились текстовым вопросом («раз в», «за заход», срок поста, слово
// вызова), — поля строками настройки: текущее значение видно, формат
// подсказан, ошибка приходит до «ОК», а сохраняет нижняя полоса. Что правят
// раз в год, — «Тонкая настройка» вкладки; ключи настроек и их умолчания те
// же, что читает движок (реестр `store.SettingDef`).
func (s *Server) tgAnswerTab(add func(Component), acc *store.TGAccount) {
	digits := engine.NumberDigits(acc.Phone)
	send := "off"
	if acc.CanSend {
		send = "on"
	}
	add(Component{Type: "section", Title: "Ответы и аккуратность · " + acc.Name(), Children: []Component{
		{Type: "row", Layout: "wide", ID: "tg-send", Subtitle: "Разрешить Сёме отправлять сообщения", Toggle: send,
			Caption: "только у этой учётки; выключено — Сёма готовит текст, но отправляете вы кнопкой",
			Actions: []Action{{Action: "tg_toggle_send", Target: digits, Label: "toggle"}}},
	}})
	add(s.tgLiveSection(acc))

	// слово-вызов и пределы молчания правят чаще остального: они стоят на самой
	// вкладке, а не в «Тонкой настройке»
	add(Component{Type: "form", ID: "tg-limits", Title: "Пределы переписки · общее для всех учёток", Style: formRows, Fields: []Field{
		s.settingField("tg.help_word", "help_word", "Слово-вызов Сёмы",
			"С него начинается просьба к Сёме внутри переписки: «Сёма, посмотри…». Написанное без него уходит собеседнику", ""),
		s.settingField("tg.reply_max_hour", "reply_max_hour", "Автоответов в час",
			"Предел на чат: бережёт от переписки двух ботов; ваши сообщения в счёт не идут", "в час"),
		s.settingField("tg.wait_warn_min", "wait_warn_min", "Когда напомнить о молчании",
			"Через сколько минут без ответа появится запись «Ждёт ответа»", "мин"),
		s.settingField("tg.wait_alert_min", "wait_alert_min", "Когда поднять тревогу",
			"Через сколько минут молчание становится тревогой", "мин"),
		s.settingField("tg.wait_msgs", "wait_msgs", "Сообщений подряд до тревоги",
			"Сколько сообщений без ответа поднимают уровень записи", "шт"),
	}, Submit: &Action{Action: "save_tg_limits", Label: "Сохранить"}})

	// редкое — «Тонкая настройка» (settings_rare.go): обращения к Telegram, объём
	// и ритм ответа правят, когда что-то не так, а не каждый день
	add(Component{Type: "form", ID: "tg-rate", Title: "Пределы обращений · " + acc.Name(), Tier: tierRare, Style: formRows, Fields: []Field{
		s.accountField("every", "Как часто заходить", strconv.Itoa(acc.Every), store.TGEveryDefault,
			"У каждой учётки свои: сторонний клиент не должен привести к блокировке", "мин"),
		s.accountField("batch", "Сколько сообщений за заход", strconv.Itoa(acc.Batch), store.TGBatchDefault,
			"При обновлении списка и заходе за перепиской", "сообщений"),
	}, Submit: &Action{Action: "save_tg_rate", Target: digits, Label: "Сохранить"}})
	add(Component{Type: "form", ID: "tg-volume", Title: "Объём и сроки · общее для всех учёток", Tier: tierRare, Style: formRows, Fields: []Field{
		s.settingField("tg.history", "history", "Сколько истории переносить",
			"При включении чата в переписку Сёмы попадут последние сообщения; 0 — не переносить", "сообщений"),
		s.settingField("tg.file_limit_mb", "file_limit_mb", "Предел вложения", "Файл крупнее Сёма не забирает", "МБ"),
		s.settingField("tg.post_days", "post_days", "Пост канала — разговор",
			"Общее для всех каналов: пост свежее этого срока становится разговором с комментариями, старше — остаётся историей канала", "дней"),
		s.settingField("tg.chats_max", "chats_max", "Сколько диалогов читать из списка",
			"Предохранитель при обновлении списка: Telegram отдаёт диалоги по сотне, включая архив, и дальше этого числа Сёма не листает", "диалогов"),
	}, Submit: &Action{Action: "save_tg_limits", Label: "Сохранить"}})
	add(Component{Type: "form", ID: "tg-stream-limits", Title: "Ответ на глазах · общее для всех учёток", Tier: tierRare, Style: formRows, Fields: []Field{
		s.settingField("tg.stream_ms", "stream_ms", "Как часто дописывать ответ",
			"Не чаще, чем раз в столько правится сообщение; ниже 100 мс Telegram начинает просить подождать", "мс"),
		s.settingField("tg.stream_first", "stream_first", "Сколько знаков до первого сообщения",
			"Пока ответ короче, он не отправляется: два слова в уведомлении на телефоне — плохое первое впечатление", "знаков"),
	}, Submit: &Action{Action: "save_tg_limits", Label: "Сохранить"}})
}

// accountField — число из самой учётки (а не из таблицы настроек): «как часто
// заходить» и «сколько за заход» у каждой учётки свои. Умолчание — то, что
// хранилище подставляет новой учётке.
func (s *Server) accountField(name, label, value string, def int, hint, unit string) Field {
	d := strconv.Itoa(def)
	return Field{Type: "number", Name: name, Label: label, Hint: hint, Unit: unit, Value: value, Min: 1, Default: &d}
}
