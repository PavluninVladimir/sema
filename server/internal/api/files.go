package api

import (
	"fmt"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Экран файлов и чеков.

// отсев — ящики, чьи вложения не должны попасть в снимок на устройстве.
// Пусто при обычной сборке: человеку закрытый ящик виден как есть.
func (s *Server) buildFiles(doc *Document, filter, search string, limit int, отсев *store.Exclusion) error {
	// первый показ — двадцать: пятьдесят строк с капсулами, фактами и меню
	// весили 1 896 узлов и 141 КБ (замер 12 сентября 2026)
	if limit == defaultRows {
		limit = 20
	}
	st := s.eng.Store()
	doc.Searchable = true
	// чек — обычный файл с суммой: отдельного экрана у чеков нет
	doc.Filters = []Filter{
		{ID: "all", Title: "Все"},
		{ID: "docs", Title: "Документы"},
		{ID: store.SourceMail, Title: "Из писем"},
		{ID: store.SourceUpload, Title: "Загрузки"},
		{ID: store.SourceModule, Title: "От модулей"},
		{ID: store.SourceWeb, Title: "Из интернета"},
	}
	if filter == "" {
		filter = "all"
	}
	doc.ActiveFilter = filter
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	doc.Heading = "Файлы"
	total, bytes, bySource := st.FileStats()
	doc.Subheading = "Файлы Сёма забирает себе целиком: из писем, из наблюдаемых папок, от модулей и из интернета"
	doc.ToolbarActions = append(doc.ToolbarActions,
		Action{Action: "$goto", Target: vendorsScreen, Label: "Поставщики"},
		Action{Action: "$goto", Target: "settings:storages", Label: "Хранилища"})
	// счётчики уехали из подписи в строку чисел: «131 файл · 1,2 ГБ ·
	// 4 источника» набиралось прозой, и ни одно число не читалось
	if total > 0 {
		число, единица := splitAmount(humanBytes(bytes))
		add(Component{Type: "figs", ID: "files-figs", Children: []Component{
			{Text: strconv.Itoa(total), Title: "файлов"},
			{Text: число, Unit: единица, Title: "занимают"},
			{Text: strconv.Itoa(len(bySource)), Title: "источников"},
		}})
	}
	add(Component{Type: "text", Style: "secondary", Icon: "arrow.down.doc",
		Text: "Перетащите файл в окно — Сёма сохранит его в хранилище, прочитает текст и разбудит модули в ту же секунду. У каждого файла видно, откуда он и где лежит."})

	query := search

	var atts []*store.Attachment
	var err error
	switch {
	case query != "":
		// ищет сервер: у клиента загружена только первая порция, и его
		// местный фильтр не видел остальные файлы вовсе
		atts, err = st.SearchFiles(query, limit)
	}
	if query == "" {
		switch filter {
		case "docs":
			atts, err = st.Attachments(store.AttKindDocument, limit)
		case store.SourceMail, store.SourceUpload, store.SourceFolder, store.SourceModule, store.SourceWeb:
			atts, err = st.FilesBySource(filter, limit)
		default:
			atts, err = st.Attachments("", limit)
		}
	}
	if err != nil {
		return err
	}
	// Отсев после выборки, а не в SQL: пустое имя ящика значит и «старое
	// письмо закрытого ящика», и «файл вообще без письма» — исключение в
	// запросе спрятало бы все загруженные человеком файлы.
	atts = st.FilterExcludedAttachments(atts, отсев)
	ids := make([]int64, len(atts))
	for i, a := range atts {
		ids[i] = a.ID
	}
	sources := st.FileSourcesFor(ids)
	codes := st.FileCodesFor(ids)
	shares := st.ActiveSharesFor(ids)
	rows := make([]Component, 0, len(atts))
	dates := make([]time.Time, 0, len(atts))
	for _, a := range atts {
		row := s.fileRow(a, sources[a.ID], codes[a.ID])
		s.addShares(&row, a, shares[a.ID])
		rows = append(rows, row)
		dates = append(dates, a.Date)
	}
	if len(rows) == 0 {
		add(emptyState(filesEmptyTitle(filter, query), filesEmptyWhy(filter, query), "doc"))
		return nil
	}
	// общий список сортируем по дате, потом расставляем дни
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if dates[j].After(dates[i]) {
				rows[i], rows[j] = rows[j], rows[i]
				dates[i], dates[j] = dates[j], dates[i]
			}
		}
	}
	countKind, countSource := "", ""
	switch filter {
	case "docs":
		countKind = store.AttKindDocument
	case store.SourceMail, store.SourceUpload, store.SourceFolder, store.SourceModule, store.SourceWeb:
		countSource = filter
	}
	// сколько всего таких файлов: «total» выше — это все файлы вообще,
	// а здесь считаем по нынешнему отбору
	inFilter := st.CountFiles(countKind, countSource)
	// В снимочной сборке это число считать нечем: запрос считает и то, что
	// мы только что отсеяли, и «Показаны 3 из 10» разом соврало бы и
	// намекнуло, что за кадром остались чьи-то файлы. Приравниваем к
	// показанному — тогда ни строки «из N», ни кнопки «Показать ещё»,
	// которой офлайн всё равно нечего подгрузить.
	if отсев != nil {
		inFilter = len(rows)
	}
	// заголовок раздела ставим только тогда, когда он что-то говорит:
	// «Файлы · 50» над списком файлов повторяло название экрана
	title := ""
	if inFilter > len(rows) {
		title = fmt.Sprintf("Показаны %d из %d", len(rows), inFilter)
	}
	if query != "" {
		title = fmt.Sprintf("Нашлось по «%s» · %s", query, plural(len(rows), "файл", "файла", "файлов"))
	}
	add(Component{Type: "section", ID: "files-list", Title: title, Children: groupRows(rows, dates)})
	// без этой кнопки до остальных файлов было не добраться вовсе: поиск
	// у клиента идёт по загруженному, а загружено было 50 из 131
	if inFilter > len(rows) {
		add(Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d из %d", len(rows), inFilter)},
		}})
	}
	return nil
}

// fileRow — файл с капсулами источников и хранилища; открывается через сервер.
func (s *Server) fileRow(a *store.Attachment, sources []store.FileSource, codes []store.FileCode) Component {
	row := attRow(a)
	addCodes(&row, codes)
	if len(sources) == 0 && a.MessageID > 0 {
		sources = []store.FileSource{{Kind: store.SourceMail, Account: a.Account}}
	}
	// одинаковые источники (один файл из трёх писем одного ящика) — одна
	// капсула со счётчиком, а не три подряд
	var names []string
	counts := map[string]int{}
	var order []Badge
	for _, src := range sources {
		var b Badge
		switch src.Kind {
		case store.SourceMail:
			b = s.mailboxBadge(src.Account)
			b.Text = "письмо · " + b.Text
		case store.SourceUpload:
			b = Badge{Text: engine.SourceTitle(src), Color: "#0058B0"}
		case store.SourceFolder:
			b = Badge{Text: engine.SourceTitle(src), Color: "#7952B3"}
		case store.SourceModule:
			b = Badge{Text: engine.SourceTitle(src), Color: "#B7791F"}
		default:
			continue
		}
		if counts[b.Text] == 0 {
			order = append(order, b)
		}
		counts[b.Text]++
		names = append(names, engine.SourceTitle(src))
	}
	for _, b := range order {
		if n := counts[b.Text]; n > 1 {
			b.Text = fmt.Sprintf("%s ×%d", b.Text, n)
		}
		row.Badges = append(row.Badges, b)
	}
	if a.Storage != "" {
		row.Badges = append(row.Badges, Badge{Text: storageBadge(a.Storage), Color: "#8E8E93"})
		if a.Mirror != "" {
			row.Badges = append(row.Badges, Badge{Text: "S3 · " + a.Mirror, Color: "#1E7A32"})
		}
	} else {
		row.Badges = append(row.Badges, Badge{Text: "ждёт переезда", Color: "#8E8E93"})
	}
	// где лежит файл — факт, а не капсула: «локально» стояло у всех
	// пятидесяти строк подряд и не отличало ни одну
	var kept []Badge
	for _, b := range row.Badges {
		switch {
		case b.Text == "локально":
			row.Facts = append(row.Facts, Prop{Label: "Хранится", Value: "локально"})
		case strings.HasPrefix(b.Text, "S3 · "):
			row.Facts = append(row.Facts, Prop{Label: "Копия", Value: strings.TrimPrefix(b.Text, "S3 · ")})
		default:
			kept = append(kept, b)
		}
	}
	row.Badges = kept
	// класс и то, кем он поставлен: без этого «модели не показывал» выглядит
	// капризом, а человек не знает, что решать
	if m, ok := s.eng.Store().ClassMarkOf(store.ClassFile, a.ID); ok && m.Class != "" && m.Class != store.ClassPlain {
		кем := "по приметам"
		if m.By != "" {
			кем = "пометил " + m.By
		}
		if m.By == "" && m.Why != "" {
			кем = m.Why // какая примета сработала: без неё спорить не с чем
		}
		row.Facts = append(row.Facts, Prop{Label: "Класс", Value: classTitle(m.Class) + " · " + кем,
			Color: classColor(m.Class)})
		if m.Class == store.ClassSecret {
			row.Facts = append(row.Facts, Prop{Label: "Модели", Value: "не показывал"})
		}
	}
	for i := range row.Actions {
		if row.Actions[i].Action == "delete_file" && len(names) > 1 {
			row.Actions[i].Confirm = "Удалить файл «" + a.Filename + "» целиком? Источники: " + strings.Join(names, "; ") + ". Письма и их текст останутся у Сёмы."
		}
	}
	return row
}

// addCodes — что видно про коды в строке файла: капсула вида
// («QR ФНС», «Честный знак», «EAN») в самой строке, разобранные поля
// парами и сырое значение — в раскрытии. Новых типов компонентов не
// нужно: капсулы, пары «подпись — значение» и раскрытие уже есть.
func addCodes(row *Component, codes []store.FileCode) {
	if len(codes) == 0 {
		return
	}
	// искали и не нашли — это тоже итог, и человек должен его видеть:
	// иначе пустое место не отличить от «даже не смотрели»
	if нет, ok := store.NotFound(codes); ok {
		искали := нет.Field("Искали")
		if искали == "" {
			искали = "искали"
		} else {
			искали = "искали " + искали
		}
		row.Facts = append(row.Facts, Prop{Label: "Коды", Value: "не найдены · " + искали})
		return
	}
	seen := map[string]bool{}
	for _, c := range codes {
		название := engine.FormatTitle(c.Format)
		if !seen[название] {
			seen[название] = true
			level := ""
			if c.Format == store.CodeSecretURL {
				level = LevelWarn // в ссылке чужой токен: человеку стоит знать
			}
			row.Badges = append(row.Badges, Badge{Text: название, Level: level})
		}
		title := engine.SymbologyTitle(c.Symbology)
		if c.Page > 0 {
			title += fmt.Sprintf(" · страница %d", c.Page)
		}
		row.Props = append(row.Props, Prop{Label: "Код", Value: title})
		for _, f := range c.Fields {
			row.Props = append(row.Props, Prop{Label: f.Label, Value: f.Value})
		}
		// сырое значение — последним: по нему сверяются, когда разбор
		// вызывает сомнения, но читать его каждый раз незачем
		if c.Format != store.CodeSecretURL {
			row.Props = append(row.Props, Prop{Label: "Значение", Value: clipRunes(c.Value, 300)})
		}
	}
	row.Expandable = true
}

func storageBadge(name string) string {
	switch {
	case name == "local":
		return "локально"
	case strings.HasPrefix(name, "missing:"):
		return "файла нет на диске"
	}
	return name
}

func attRow(a *store.Attachment) Component {
	from := a.FromName
	if from == "" {
		from = a.FromAddr
	}
	badge, color := "", ""
	if a.Kind == store.AttKindReceipt && a.Amount > 0 {
		cur := a.Currency
		if cur == "" || cur == "RUB" {
			cur = "₽"
		}
		badge, color = fmt.Sprintf("%.0f %s", a.Amount, cur), "green"
	}
	ext := strings.TrimPrefix(strings.ToUpper(filepath.Ext(a.Filename)), ".")
	if ext == "" {
		ext = "FILE"
	}
	if len(ext) > 4 {
		ext = ext[:4]
	}
	// размер, дата и извлечённый текст — факты, а не склейка в подписи;
	// строка открывается тапом, кнопка «Открыть» не нужна
	// день уже стоит заголовком группы, а «текст извлечён» стоял у всех
	// пятидесяти строк подряд и не отличал ни одну: у 51 строки было
	// 2 014 узлов, и половина из них — повторяющиеся факты и кнопки
	facts := []Prop{
		{Label: "Размер", Value: humanBytes(a.Size)},
		{Label: "Когда", Value: a.Date.Format("15:04")},
	}
	if from != "" {
		facts = append(facts, Prop{Label: "Откуда", Value: from})
	}
	if a.Extracted == "" {
		facts = append(facts, Prop{Label: "Текст", Value: "не извлечён"})
	}
	return Component{
		Type:        "row",
		ID:          "att-" + strconv.FormatInt(a.ID, 10),
		Subtitle:    a.Filename,
		Caption:     strings.TrimSpace(fileSource(a) + a.Summary),
		Facts:       facts,
		Badge:       badge,
		BadgeColor:  color,
		Avatar:      ext,
		AvatarColor: fileColor(ext),
		AvatarShape: "file",
		Actions:     fileActions(a),
	}
}

// fileActions — открыть через сервер ($file), письмо (если есть), удалить.
func fileActions(a *store.Attachment) []Action {
	// «$file» кнопкой не рисуется: по нему кликается сама строка (см. toBlock)
	acts := []Action{{Action: "$file", Target: strconv.FormatInt(a.ID, 10), Label: "Открыть"}}
	if a.MessageID > 0 {
		acts = append(acts, Action{Action: "$goto", Target: "msg:" + strconv.FormatInt(a.MessageID, 10), Label: "Письмо"})
	}
	return append(acts, Action{Action: "delete_file", Target: strconv.FormatInt(a.ID, 10), Label: "Удалить",
		Style: "destructive", ConfirmOK: "Удалить",
		Confirm: "Удалить файл «" + a.Filename + "» из хранилища? Письмо и его текст останутся у Сёмы."})
}

func fileColor(ext string) string {
	switch ext {
	case "PDF":
		return "#D2372B"
	case "XLSX", "XLS", "CSV":
		return "#4E9A51"
	case "DOC", "DOCX", "RTF":
		return "#2E7DD1"
	case "PNG", "JPG", "JPEG", "HEIC", "GIF", "WEBP":
		return "#7952B3"
	case "ZIP", "RAR", "7Z":
		return "#C7532F"
	default:
		return "#5A6ACF"
	}
}

// fileSource — откуда файл: письмо, ящик; человек должен видеть источник,
// а не только имя файла.
func fileSource(a *store.Attachment) string {
	if a.MessageID == 0 {
		return "" // файл без письма: источник виден капсулой
	}
	src := "из письма"
	if a.Subject != "" {
		src += " «" + clipRunes(a.Subject, 60) + "»"
	}
	if a.Summary == "" {
		return src
	}
	return src + "\n"
}

// classTitle и classLevel — как класс выглядит человеку. Слова те же, что на
// экране письма: одно понятие не должно называться в двух местах по-разному.
func classTitle(class string) string {
	switch class {
	case store.ClassSecret:
		return "секретное"
	case store.ClassSensitive:
		return "чувствительное"
	}
	return "обычное"
}

// цвета те же, что на экране «Что ушло»
func classColor(class string) string {
	switch class {
	case store.ClassSecret:
		return "#C4271C"
	case store.ClassSensitive:
		return "#B25000"
	}
	return ""
}

// filesEmptyTitle и filesEmptyWhy — пустое место объясняет себя по тому
// отбору, на котором человек стоит: «Файлов пока нет» на вкладке
// «От модулей» звучало так, будто файлов нет вовсе.
func filesEmptyTitle(filter, query string) string {
	if query != "" {
		return "По «" + query + "» ничего не нашлось"
	}
	switch filter {
	case "docs":
		return "Документов пока нет"
	case store.SourceMail:
		return "Из писем ничего не приходило"
	case store.SourceUpload:
		return "Вы ещё ничего не загружали"
	case store.SourceModule:
		return "Модули пока ничего не сохранили"
	case store.SourceWeb:
		return "Страниц из интернета пока нет"
	}
	return "Файлов пока нет"
}

func filesEmptyWhy(filter, query string) string {
	if query != "" {
		return "искали по имени, теме письма, извлечённому тексту и кодам с картинки («ФД 4719», номер товара) — попробуйте короче или снимите отбор"
	}
	switch filter {
	case "docs":
		return "сюда попадают PDF, docx, xlsx и прочее, из чего Сёма достаёт текст"
	case store.SourceMail:
		return "вложения Сёма забирает сам, как только разберёт письмо"
	case store.SourceUpload:
		return "перетащите файл прямо в окно — он ляжет в хранилище и станет виден моделям"
	case store.SourceModule:
		return "модуль с правом «файлы» кладёт сюда то, что сделал сам"
	case store.SourceWeb:
		return "страницы по ссылкам из поиска сохраняются сюда текстом"
	}
	return "они появятся сами из писем и наблюдаемых папок — или перетащите свой прямо в окно"
}

// splitAmount делит «1,2 ГБ» на число и единицу: в строке чисел единица
// набирается мельче самого числа, а склеенная — спорит с ним.
func splitAmount(s string) (число, единица string) {
	if i := strings.LastIndex(s, " "); i > 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}
