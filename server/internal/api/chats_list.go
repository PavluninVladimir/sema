package api

// Список чатов экраном — так его видит телефон. На широком экране список
// живёт панелью слева, а разговор справа; в 390 точках две колонки не
// помещаются, и список прятался за бургером на 288 точек: «Анна |
// Закупки из Кита…» и «Перепиши модуль serve…» обрезались на середине.
//
// Здесь тот же список, но строками во всю ширину: лицо кружком, имя,
// превью в две строки, время справа. Ветка «Сёма» под разговором — своя
// строка со счётчиком: то, что Сёма не смог ответить, не должно ждать,
// пока откроют нужный чат.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// avatarColors — палитра кружков. Цвет выбирается по имени и не меняется:
// чат узнают по цвету раньше, чем читают название.
var avatarColors = []string{"#1F4FD8", "#2E7D4F", "#9C6100", "#BE3320", "#585B64", "#6B3FA0"}

// avatarColor — устойчивый цвет по имени.
func avatarColor(name string) string {
	var сумма int
	for _, р := range name {
		сумма += int(р)
	}
	return avatarColors[сумма%len(avatarColors)]
}

// chatInitials — одна-две буквы для кружка. Из «Анна | Закупки из
// Китая» берём «АЗ»: первые буквы двух первых слов, а знаки-разделители
// в инициалы не годятся.
func chatInitials(name string) string {
	var буквы []rune
	for _, слово := range strings.FieldsFunc(name, func(р rune) bool {
		return р == ' ' || р == '|' || р == '·' || р == '/' || р == ',' || р == '-'
	}) {
		р := []rune(слово)
		if len(р) == 0 || !letter(р[0]) {
			continue
		}
		буквы = append(буквы, р[0])
		if len(буквы) == 2 {
			break
		}
	}
	if len(буквы) == 0 {
		return "?"
	}
	return strings.ToUpper(string(буквы))
}

func letter(р rune) bool {
	return (р >= 'а' && р <= 'я') || (р >= 'А' && р <= 'Я') || р == 'ё' || р == 'Ё' ||
		(р >= 'a' && р <= 'z') || (р >= 'A' && р <= 'Z') || (р >= '0' && р <= '9')
}

// chatChips — по каким источникам делится список. Те же группы, что и в
// панели: их человек уже знает.
var chatChips = []string{"Telegram", "MAX", "Разбор уведомлений", "Задачи", "Мои чаты"}

// buildChatsList — экран списка чатов для телефона.
func (s *Server) buildChatsList(doc *Document, filter, search string) error {
	st := s.eng.Store()
	chats, err := st.Chats()
	if err != nil {
		return err
	}
	doc.Heading = "Чаты"
	doc.Layout = "wide"
	doc.Searchable = true
	doc.ToolbarActions = []Action{{Action: "new_chat", Label: "Новый чат", Style: "primary"}}

	kids := map[int64][]*store.Chat{}
	var roots []*store.Chat
	for _, c := range chats {
		if c.Parent > 0 {
			kids[c.Parent] = append(kids[c.Parent], c)
			continue
		}
		roots = append(roots, c)
	}
	// у контейнера своих сообщений нет: его место в списке задаёт самый
	// свежий разговор под ним
	for _, c := range roots {
		if !c.IsContainer() {
			continue
		}
		for _, k := range kids[c.ID] {
			if k.LastAt.After(c.LastAt) {
				c.LastAt = k.LastAt
			}
		}
	}
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].LastAt.After(roots[j].LastAt) })

	// чипы источников: у пустой группы чипа нет — фильтр, который ничего
	// не находит, врёт про содержимое
	has := map[string]bool{}
	for _, c := range roots {
		has[chatGroup(c.Title)] = true
	}
	doc.Filters = []Filter{{ID: "", Title: "Все"}}
	for _, г := range chatChips {
		if has[г] {
			doc.Filters = append(doc.Filters, Filter{ID: г, Title: г})
		}
	}
	doc.ActiveFilter = filter

	req := strings.ToLower(strings.TrimSpace(search))
	черновики := st.TGDraftCounts()
	var строки []Component
	for _, c := range roots {
		г := chatGroup(c.Title)
		if filter != "" && filter != г {
			continue
		}
		if req != "" && !chatMatches(c, kids[c.ID], req) {
			continue
		}
		строки = append(строки, s.chatRow(c, kids[c.ID], г, черновики))
		// ветки видны в списке, когда в них что-то ждёт: черновик,
		// который не уйдёт без кнопки, или непрочитанный ответ
		for _, к := range kids[c.ID] {
			if к.IsThread() && (s.chatUnread(к) || черновики[к.ID] > 0) {
				строки = append(строки, s.threadRow(к, черновики))
			}
		}
	}
	if len(строки) == 0 {
		почему := "Свои чаты, переписка из мессенджеров и ветки «Сёма» под ними. Начните с «Нового чата»: спросите про письма, файлы или серверы — Сёма помнит разговор и видит ваш архив."
		if req != "" || filter != "" {
			почему = "Ничего не нашлось. Ищется по названию чата и по последней реплике; чип сверху сужает список до одного источника."
		}
		doc.Components = append(doc.Components, emptyState("Здесь будут разговоры", почему, "bubble.left.and.bubble.right"))
		return nil
	}
	doc.Components = append(doc.Components, Component{Type: "section", Children: строки})
	return nil
}

// chatGroup — к какому источнику относится чат. Та же мерка, что у
// панели на широком экране: список один, и делиться он должен одинаково.
func chatGroup(title string) string {
	switch {
	case strings.HasPrefix(title, "Telegram · "):
		return "Telegram"
	case strings.HasPrefix(title, "MAX · "):
		return "MAX"
	case strings.HasPrefix(title, "Уведомление · "):
		return "Разбор уведомлений"
	case strings.HasPrefix(title, "Задача"):
		return "Задачи"
	}
	return "Мои чаты"
}

// chatMatches — ищем по названию и по последней реплике, а у контейнера
// ещё и по названиям тем: группу ищут по теме не реже, чем по имени.
func chatMatches(c *store.Chat, children []*store.Chat, req string) bool {
	стог := strings.ToLower(c.Title + " " + c.LastMsg)
	for _, к := range children {
		стог += " " + strings.ToLower(к.Title+" "+к.LastMsg)
	}
	for _, слово := range strings.Fields(req) {
		if !strings.Contains(стог, слово) {
			return false
		}
	}
	return true
}

// chatRow — разговор или контейнер строкой списка.
// черновики — TGDraftCounts на весь список: один запрос, а не по чату.
func (s *Server) chatRow(c *store.Chat, children []*store.Chat, группа string, черновики map[int64]int) Component {
	name := strings.TrimPrefix(strings.TrimPrefix(c.Title, "Telegram · "), "MAX · ")
	row := Component{
		Type: "row", Layout: "wide", ID: "чат-" + strconv.FormatInt(c.ID, 10),
		Subtitle: name, Meta: whenShort(c.LastAt), MetaInline: true,
		Avatar: chatInitials(name), AvatarColor: avatarColor(name),
		Actions: []Action{{Action: "$goto", Target: "chat:" + strconv.FormatInt(c.ID, 10), Label: "Открыть"}},
	}
	if c.IsContainer() {
		n := 0
		var last time.Time
		for _, к := range children {
			if к.IsThread() {
				continue
			}
			n++
			if к.LastAt.After(last) {
				last = к.LastAt
			}
		}
		один, пара, много := "тема", "темы", "тем"
		row.Caption = plural(n, один, пара, много)
		row.Meta = whenShort(last)
		return row
	}
	row.Caption = chatPreview(c.LastMsg, группа)
	switch {
	case черновики[c.ID] > 0:
		// черновик собеседнику не уйдёт без кнопки: это не «новое», а
		// «не поедет без вас», и в списке видно разницу
		row.Level = LevelAlert
		row.Badges = []Badge{{Text: "ждёт отправки", Level: LevelAlert}}
	case s.chatUnread(c):
		row.Level = LevelInfo
		row.Dot = levelDot(LevelInfo)
	}
	return row
}

// threadRow — ветка «Сёма» под разговором: своя строка со сдвигом,
// чтобы было видно, чья она.
func (s *Server) threadRow(т *store.Chat, черновики map[int64]int) Component {
	row := Component{
		Type: "row", Layout: "wide", Style: "nested", ID: "ветка-" + strconv.FormatInt(т.ID, 10),
		Icon:     "bubble.left.and.text.bubble.right",
		Subtitle: s.eng.ThreadTitle(т), Caption: chatPreview(т.LastMsg, "Мои чаты"),
		Meta:    whenShort(т.LastAt),
		Actions: []Action{{Action: "$goto", Target: "chat:" + strconv.FormatInt(т.ID, 10), Label: "Открыть"}},
	}
	if n := черновики[т.ID]; n > 0 {
		row.Level = LevelAlert
		row.Badges = []Badge{{Text: fmt.Sprintf("черновиков %d", n), Level: LevelAlert}}
		return row
	}
	row.Level = LevelInfo
	row.Dot = levelDot(LevelInfo)
	return row
}
