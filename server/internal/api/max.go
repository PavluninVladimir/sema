package api

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Вкладка «MAX»: вход по телефону и коду, выбор чатов и права по каждому
// собеседнику — по образцу Telegram, потому что правила одни: ничего не
// собирается, пока человек не включил чат, и Сёма не пишет за него.

const maxScreen = "settings:max"

func (s *Server) buildMAX(doc *Document) error {
	doc.Heading = "MAX"
	doc.Subheading = "Личные чаты MAX от вашего имени: что Сёма читает и где отвечает"
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	st := s.eng.Store()

	add(Component{Type: "text", Style: "secondary",
		Text: "Аккаунт MAX как источник, рядом с почтой и Telegram. Клиент работает от вашего имени: " +
			"токен доступа лежит в базе зашифрованным и наружу не выдаётся — ни модели, ни модулю."})

	acc, has := st.MAXAccountGet()
	state := "new"
	if has {
		state = acc.State
	}
	switch state {
	case "new", "fail":
		row := Component{Type: "row", Layout: "wide", Dot: "#8E8E93", Subtitle: "Аккаунт не подключён",
			Caption: "MAX пришлёт код в SMS"}
		if has && acc.Note != "" {
			row.Dot, row.Caption = "#C4271C", acc.Note
		}
		add(Component{Type: "section", Title: "Вход", Children: []Component{row, s.maxCertRow()}})
		add(Component{Type: "section", Title: "Проще всего", Children: []Component{{
			Type: "row", Layout: "wide", ID: "max-web", Level: LevelOK,
			Subtitle: "Войти страницей MAX",
			Caption: "откроется web.max.ru внутри окна: там вы проходите проверку и вводите код из SMS, " +
				"а Сёма забирает ключ доступа со страницы — вручную ничего копировать не нужно",
			Actions: []Action{{Action: "$weblogin", Target: "https://web.max.ru",
				Value: "max_set_token", Label: "Открыть вход MAX", Style: "primary"}}}}})
		add(Component{Type: "form", ID: "max-phone", Title: "Или по телефону",
			Fields:  []Field{{Type: "text", Name: "phone", Label: "Телефон", Hint: "с кодом страны: +79991234567"}},
			Actions: []Action{{Action: "max_send_code", Label: "Прислать код", Style: "primary"}}})
		add(Component{Type: "form", ID: "max-token", Title: "Или вставить токен из браузера",
			Fields: []Field{
				{Type: "text", Name: "phone", Label: "Телефон", Value: phoneOf(acc, has)},
				{Type: "secure", Name: "token", Label: "Токен доступа",
					Hint: "войдите на web.max.ru в браузере и возьмите токен оттуда — проверка VK отдаёт его только своей странице"},
			},
			Actions: []Action{{Action: "max_set_token", Label: "Войти по токену", Style: "primary"}}})
		return nil
	case "captcha":
		link := s.eng.MAXCaptchaLink()
		add(Component{Type: "section", Title: "Вход", Children: []Component{
			{Type: "row", Layout: "wide", Level: LevelWarn, Dot: levelDot(LevelWarn), Subtitle: acc.Phone,
				Badges:  []Badge{{Text: "нужна проверка VK", Color: "#B25000"}},
				Caption: acc.Note,
				Actions: []Action{
					{Action: "$open", Target: link, Label: "Открыть проверку", Style: "primary"},
					{Action: "$copy", Target: link, Label: "Скопировать ссылку"},
					{Action: "max_forget", Label: "Отменить", Style: "destructive"},
				}},
			s.maxCertRow(),
		}})
		add(Component{Type: "section", Title: "Проще всего", Children: []Component{{
			Type: "row", Layout: "wide", ID: "max-web", Level: LevelOK,
			Subtitle: "Войти страницей MAX",
			Caption:  "проверка и код из SMS проходят на самой странице MAX внутри окна Сёмы",
			Actions: []Action{{Action: "$weblogin", Target: "https://web.max.ru",
				Value: "max_set_token", Label: "Открыть вход MAX", Style: "primary"}}}}})
		add(Component{Type: "form", ID: "max-captcha", Title: "После проверки",
			Fields: []Field{
				{Type: "text", Name: "phone", Label: "Телефон", Value: acc.Phone},
				{Type: "text", Name: "captcha", Label: "Код проверки",
					Hint: "то, что вернула проверка VK (captchaToken); если проверка прошла молча — оставьте пустым и нажмите ещё раз"},
			},
			Actions: []Action{{Action: "max_send_code", Label: "Прислать код", Style: "primary"}}})
		add(Component{Type: "form", ID: "max-token", Title: "Или вставить токен из браузера",
			Fields: []Field{
				{Type: "text", Name: "phone", Label: "Телефон", Value: acc.Phone},
				{Type: "secure", Name: "token", Label: "Токен доступа",
					Hint: "войдите на web.max.ru в браузере и возьмите токен оттуда — проверка VK отдаёт его только своей странице"},
			},
			Actions: []Action{{Action: "max_set_token", Label: "Войти по токену", Style: "primary"}}})
		return nil
	case "code":
		add(Component{Type: "section", Title: "Вход", Children: []Component{{
			Type: "row", Layout: "wide", Level: LevelWarn, Dot: levelDot(LevelWarn), Subtitle: acc.Phone,
			Badges:  []Badge{{Text: "ждёт код", Color: "#B25000"}},
			Caption: acc.Note + "\nтокен ляжет в базу зашифрованным",
			Actions: []Action{
				{Action: "max_send_code", Target: acc.Phone, Label: "Прислать снова"},
				{Action: "max_forget", Label: "Отменить", Style: "destructive"}}}}})
		add(Component{Type: "form", ID: "max-code", Title: "Код из SMS",
			Fields:  []Field{{Type: "text", Name: "code", Label: "Код"}},
			Actions: []Action{{Action: "max_confirm_code", Label: "Войти", Style: "primary"}}})
		return nil
	}

	// вошли: аккаунт, связь, право отправки — три строки, а не одна на
	// всё. В одной строке телефон, «на связи», тумблер, две кнопки и
	// капсула спорили за место, и на телефоне она не помещалась
	add(Component{Type: "section", Title: "Аккаунт", Children: []Component{
		{Type: "row", Layout: "wide", ID: "max-acc", Subtitle: acc.Phone, Caption: acc.Note,
			Menu: []Action{
				{Action: "max_refresh_chats", Label: "Обновить список чатов"},
				{Action: "max_forget", Label: "Забыть аккаунт", Style: "destructive",
					Confirm: "Забыть аккаунт MAX? Токен сотрётся, собранные сообщения останутся.", ConfirmOK: "Забыть"},
			}},
		s.linkRow("max-link", "MAX"),
		{Type: "row", Layout: "wide", ID: "max-send", Subtitle: "Отправка от вашего имени",
			Caption: "выключено — Сёма готовит текст, а отправляете вы",
			Toggle:  map[bool]string{true: "on", false: "off"}[acc.CanSend],
			Actions: []Action{{Action: "max_toggle_send", Label: "toggle"}}},
	}})

	chats, err := st.MAXChats(false)
	if err != nil {
		return err
	}
	if len(chats) == 0 {
		add(Component{Type: "section", Title: "Чаты", Children: []Component{emptyState(
			"Список чатов пуст", "Нажмите «Обновить список чатов» — Сёма спросит их у MAX и покажет здесь.", "bubble.left.and.bubble.right")}})
		return nil
	}
	chosen := 0
	for _, c := range chats {
		if c.Collect {
			chosen++
		}
	}
	// сами чаты приезжают при раскрытии: свёрнутый список из сорока
	// чатов с четырьмя тумблерами у каждого весил две тысячи узлов,
	// которых человек не видел. Короткий список открыт сразу
	add(Component{Type: "section", ID: "max-chats",
		Title:     fmt.Sprintf("Чаты · выбрано %d из %d", chosen, len(chats)),
		Collapsed: true, Expanded: len(chats) <= fragmentLimit,
		Lazy:       "max-chats",
		SearchHint: "Имя чата или вид: личный, группа, канал"})
	return nil
}

// linkRow — на связи ли живое соединение, а если нет — с каких пор
// и когда вернётся. Раньше «на связи» стояло капсулой навсегда: экран
// не знал, что MAX рвётся каждую минуту.
func (s *Server) linkRow(id, name string) Component {
	наСвязи, обрывов, last, пауза := s.eng.LinkState(name)
	c := Component{Type: "row", Layout: "wide", ID: id, Subtitle: "Связь"}
	if наСвязи {
		c.Caption = "на связи: сообщения приходят сразу"
		withLevel(&c, LevelOK)
		return c
	}
	c.Caption = fmt.Sprintf("оборвалась %s, попробую снова через %s · обрывов подряд %d",
		whenText(last.Format("2006-01-02T15:04:05Z07:00")), humanSeconds(пауза.Seconds()), обрывов)
	withLevel(&c, LevelWarn)
	return c
}

// maxChatsFragment — список чатов: выбранные сверху, первые двадцать, по
// поиску — имя чата или вид словом. Поиск на сервере, а не среди
// загруженного: иначе искать можно было бы только в первых сорока.
func (s *Server) maxChatsFragment(_ string, search string, предел int) ([]Component, error) {
	st := s.eng.Store()
	chats, err := st.MAXChats(false)
	if err != nil {
		return nil, err
	}
	виды := map[string]string{"dialog": "личный", "chat": "группа", "channel": "канал"}
	var подходят []*store.MAXChat
	for _, c := range chats {
		if search == "" || looksLike(search, c.Title, виды[c.Kind]) {
			подходят = append(подходят, c)
		}
	}
	// список уже отсортирован хранилищем: выбранные сверху, потом по имени
	показать := подходят
	if len(показать) > предел {
		показать = показать[:предел]
	}
	if len(показать) == 0 {
		return []Component{{Type: "row", Layout: "wide", Subtitle: "Ничего не нашлось",
			Caption: "по «" + search + "» — ни имени чата, ни вида"}}, nil
	}
	// права — матрицей, как у Telegram (chat_rights.go): строка — чат, колонка —
	// право, клетка зовёт то же `max_chat_right`, что звал тумблер. У MAX права
	// четыре и у всех видов чата одни и те же, поэтому клеток «na» нет
	var rows []rightsRow
	for _, c := range показать {
		id := strconv.FormatInt(c.ID, 10)
		class := c.Class
		if class == "" {
			class = store.ClassSensitive
		}
		flag := func(right, what string, on bool) Cell {
			return rightCell(c.Title, what, on, Action{Action: "max_chat_right", Target: id + "~" + right})
		}
		row := rightsRow{ID: "max-" + id, Title: c.Title,
			Badges: []Badge{
				{Text: виды[c.Kind], Color: "#8E8E93"},
				{Text: class, Color: map[bool]string{true: "#8E8E93", false: "#B25000"}[class == store.ClassPlain]},
			},
			Cells: map[string]Cell{
				"collect": flag("collect", "читать переписку: сообщения идут в архив и в контекст", c.Collect),
				"files":   flag("files", "забирать файлы в «Файлы» Сёмы", c.Files),
				"send":    flag("send", "писать первым: Сёма может начать разговор сам", c.Send),
				"blocked": flag("blocked", "не трогать вовсе: ни читать, ни отвечать, что бы ни просили", c.Blocked),
			}}
		if c.Count > 0 {
			row.Caption = "собрано " + strconv.Itoa(c.Count)
		}
		if c.Collect {
			row.Level = LevelOK
		}
		if c.Blocked {
			row.Level = LevelWarn
		}
		rows = append(rows, row)
	}
	out := []Component{rightsMatrix("max-matrix", "",
		"Нажатие переключает право. Ничего не включается само: разрешает человек. Блокировка снимает остальные права.",
		maxRightColumns, rows)}
	if len(подходят) > len(показать) {
		out = append(out, moreInFragment(len(показать), len(подходят)))
	}
	return out, nil
}

// maxRightColumns — колонки матрицы прав MAX по порядку.
var maxRightColumns = []rightsColumn{
	{"collect", "Читать"},
	{"files", "Файлы"},
	{"send", "Писать первым"},
	{"blocked", "Не трогать"},
}

// looksLike — есть ли слова запроса в любом из полей, без учёта регистра.
// Каждое слово запроса должно найтись хоть где-то: «группа ozon»
// находит группу с «ozon» в имени.
func looksLike(req string, поля ...string) bool {
	var всё []string
	for _, п := range поля {
		всё = append(всё, strings.ToLower(п))
	}
	for _, слово := range strings.Fields(strings.ToLower(req)) {
		нашлось := false
		for _, п := range всё {
			if strings.Contains(п, слово) {
				нашлось = true
				break
			}
		}
		if !нашлось {
			return false
		}
	}
	return true
}

// maxCertRow — тумблер «принимать просроченный сертификат». Часть узлов
// MAX отдаёт сертификат, истёкший 4 сентября 2026, и вход через них не
// проходит. Подлинность сервера проверяется в любом случае: без срока,
// но с цепочкой и именем — подменить ws-api.oneme.ru всё равно нельзя.
func (s *Server) maxCertRow() Component {
	on := s.eng.MAXAllowExpired()
	toggle, level, value := "off", LevelOK, "проверяется полностью"
	if on {
		toggle, level, value = "on", LevelWarn, "срок не проверяется"
	}
	return Component{Type: "row", Layout: "wide", ID: "max-cert", Level: level,
		Subtitle: "Принимать просроченный сертификат",
		Caption: "у части узлов MAX сертификат истёк 4 сентября — с этой галочкой вход через них проходит; " +
			"цепочка и имя сервера проверяются по-прежнему",
		Facts:   []Prop{{Label: "Сертификат", Value: value}},
		Toggle:  toggle,
		Actions: []Action{{Action: "max_toggle_cert", Label: "toggle"}}}
}

// phoneOf — номер для формы: у нового аккаунта его ещё нет.
func phoneOf(acc *store.MAXAccount, has bool) string {
	if has {
		return acc.Phone
	}
	return ""
}

func (s *Server) maxActions(req actionRequest) (string, error) {
	st := s.eng.Store()
	switch req.Action {
	case "max_send_code":
		phone := strings.TrimSpace(req.Params["phone"])
		if phone == "" {
			phone = req.Target
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return maxScreen, s.eng.MAXStartLogin(ctx, phone, req.Params["captcha"])
	case "max_confirm_code":
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return maxScreen, s.eng.MAXConfirmCode(ctx, req.Params["code"])
	case "max_set_token":
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return maxScreen, s.eng.MAXSetToken(ctx, req.Params["phone"], req.Params["token"])
	case "max_forget":
		return maxScreen, s.eng.MAXForget()
	case "max_toggle_cert":
		return maxScreen, s.eng.SetMAXAllowExpired(!s.eng.MAXAllowExpired())
	case "max_toggle_send":
		acc, ok := st.MAXAccountGet()
		if !ok {
			return "", fmt.Errorf("аккаунт не подключён")
		}
		acc.CanSend = !acc.CanSend
		return maxScreen, st.SaveMAXAccount(acc)
	case "max_refresh_chats":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		n, err := s.eng.MAXRefreshChats(ctx)
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		s.lastCheck = fmt.Sprintf("MAX: чатов в списке %d", n)
		s.mu.Unlock()
		return maxScreen, nil
	case "max_chat_right":
		target, right, ok := strings.Cut(req.Target, "~")
		if !ok {
			return "", fmt.Errorf("нужны чат и право")
		}
		id, err := strconv.ParseInt(target, 10, 64)
		if err != nil {
			return "", fmt.Errorf("нужен номер чата")
		}
		c, found := st.MAXChat(id)
		if !found {
			return "", fmt.Errorf("чата %d нет", id)
		}
		now := map[string]bool{"collect": c.Collect, "send": c.Send,
			"files": c.Files, "blocked": c.Blocked}[right]
		if right == "collect" {
			if err := s.eng.MAXSetCollect(id, !now); err != nil {
				return "", err
			}
			if !now {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if _, err := s.eng.MAXOpenChat(ctx, id); err != nil {
					log.Printf("max: чат «%s» не открыт: %v", c.Title, err)
				}
			}
			return maxScreen, nil
		}
		if err := st.SetMAXChatRight(id, right, !now); err != nil {
			return "", err
		}
		// заблокировали — снимаем всё остальное, как в Telegram
		if right == "blocked" && !now {
			for _, r := range []string{"collect", "send", "files"} {
				_ = st.SetMAXChatRight(id, r, false)
			}
		}
		return maxScreen, nil
	}
	return "", errUnhandled
}
