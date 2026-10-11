package api

// Экран «Поставщики». Сёма сводит написания только там, где уверена: «АО
// "Горэнерго"» и «ОП Городское отделение АО Горэнерго» дают
// один ключ и складываются сами. «Т Плюс ПАО» и «ТП ПАО» — не дают, и
// догадываться за человека Сёма не станет: сложить деньги разных
// поставщиков хуже, чем показать две строки.
//
// Поэтому экран нужен ровно для двух дел: слить руками то, что Сёма не
// решилась слить, и переименовать поставщика в то, как его зовёт человек.
// Оба действия ничего не портят — сырое имя остаётся на каждом документе.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const vendorsScreen = "vendors"

func (s *Server) buildVendors(doc *Document, focus string) error {
	doc.Heading = "Поставщики"
	doc.Layout = "wide"
	doc.Searchable = true
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	list, err := s.eng.Store().Vendors()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		add(emptyState("Поставщиков пока нет",
			"Сюда попадают те, чьи счета и чеки Сёма разобрал: имя из документа сводится с прежними написаниями. "+
				"Разбор файлов включается на «Хранилищах»", "building.2"))
		return nil
	}
	if focus != "" {
		if id, err := strconv.ParseInt(focus, 10, 64); err == nil {
			return s.buildVendor(doc, id)
		}
	}

	total, сумма := 0, 0.0
	for _, v := range list {
		total += v.Files
		сумма += v.Sum
	}
	doc.Subheading = fmt.Sprintf("%s · %s на %s",
		plural(len(list), "поставщик", "поставщика", "поставщиков"),
		plural(total, "документ", "документа", "документов"), moneyLine(сумма))

	var rows []Component
	for _, v := range list {
		label := plural(v.Files, "документ", "документа", "документов")
		if v.Sum > 0 {
			label += " · " + moneyLine(v.Sum)
		}
		// написания видны прямо в строке: по ним понятно, что именно
		// слилось, и не пришлось ли слить лишнего
		if len(v.Names) > 1 {
			label += " · написаний: " + strconv.Itoa(len(v.Names))
		}
		r := Component{Type: "row", ID: "vendor-" + strconv.FormatInt(v.ID, 10),
			Subtitle: v.Name, Caption: label, Layout: "wide",
			Actions: []Action{
				{Action: "$goto", Target: vendorsScreen + "@" + strconv.FormatInt(v.ID, 10), Label: "Документы"},
			},
			Selects: []Select{{
				Name: "merge", Label: "Слить с", Value: "—",
				Options: append([]string{"—"}, vendorNames(list, v.ID)...),
				Action:  Action{Action: "merge_vendor", Target: strconv.FormatInt(v.ID, 10)},
			}},
		}
		if len(v.Names) > 1 {
			r.Props = []Prop{{Label: "В документах", Value: strings.Join(v.Names, " · ")}}
		}
		rows = append(rows, r)
	}
	add(Component{Type: "section", Title: "Кто присылает документы", Children: rows})
	add(Component{Type: "text", Style: "secondary",
		Text: "Сёма сводит написания сама, только когда они совпадают после снятия «ООО», «АО» и кавычек. " +
			"«Т Плюс ПАО» и «ТП ПАО» она не сведёт: догадаться нельзя, а ошибка сложит деньги разных поставщиков. " +
			"Слияние и переименование ничего не портят — в каждом документе остаётся то имя, которым он подписан."})
	return nil
}

// buildVendor — один поставщик: его документы и его написания.
func (s *Server) buildVendor(doc *Document, id int64) error {
	v, ok := s.eng.Store().VendorByID(id)
	if !ok {
		return fmt.Errorf("поставщика %d нет", id)
	}
	doc.Heading = v.Name
	doc.Subheading = fmt.Sprintf("%s на %s", plural(v.Files, "документ", "документа", "документов"), moneyLine(v.Sum))
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	// обратный путь над заголовком: заголовок у каждого поставщика свой, и
	// без пути непонятно, откуда сюда пришли
	add(Component{Type: "crumb", Title: v.Name,
		Actions: []Action{{Action: "$goto", Target: vendorsScreen, Label: "Поставщики"}}})

	add(Component{Type: "form", ID: "vendor-name", Title: "Как его называть",
		Fields: []Field{{Type: "text", Name: "name", Label: "Имя", Value: v.Name,
			Hint: "только для списков и сумм: в самих документах имя остаётся тем, которым они подписаны"}},
		Submit: &Action{Action: "rename_vendor", Target: strconv.FormatInt(id, 10), Label: "Сохранить"}})

	if len(v.Names) > 0 {
		add(Component{Type: "section", Title: "Как он подписан в документах",
			Children: []Component{{Type: "text", Style: "secondary", Text: strings.Join(v.Names, "\n")}}})
	}

	files, err := s.eng.Store().FilesOfVendor(id, 200)
	if err != nil {
		return err
	}
	var rows []Component
	for _, f := range files {
		rows = append(rows, documentLine(f))
	}
	if len(rows) == 0 {
		rows = append(rows, emptyState("Документов нет",
			"Все документы этого поставщика отвязали или удалили", "doc"))
	}
	add(Component{Type: "section", Title: "Документы", Children: rows})
	return nil
}

// documentLine — счёт или чек в списке поставщика: сумма, срок и то,
// чем он является. Разница между «счёт» и «чек» здесь главная: по ней
// видно, ждёт ли что-то оплаты.
func documentLine(f *store.Attachment) Component {
	что := "чек"
	if f.ToPay {
		что = "счёт к оплате"
	}
	части := []string{что}
	if f.Amount > 0 {
		части = append(части, moneyLine(f.Amount))
	}
	if f.DocDate != "" {
		части = append(части, "от "+f.DocDate)
	}
	if f.DueDate != "" && f.ToPay {
		части = append(части, "до "+f.DueDate)
	}
	r := Component{Type: "row", ID: "vfile-" + strconv.FormatInt(f.ID, 10),
		Subtitle: f.Filename, Caption: strings.Join(части, " · "),
		Actions: []Action{{Action: "$file", Target: strconv.FormatInt(f.ID, 10), Label: "Открыть"}}}
	if f.ToPay && overdue(f.DueDate) {
		r.Level = LevelWarn
	}
	// сырое имя показываем, когда оно отличается от имени поставщика:
	// иначе строка врёт, будто в документе написано именно так
	if f.Vendor != "" {
		r.Props = []Prop{{Label: "В документе", Value: f.Vendor}}
	}
	return r
}

func vendorNames(list []*store.Vendor, кроме int64) []string {
	var out []string
	for _, v := range list {
		if v.ID != кроме {
			out = append(out, v.Name)
		}
	}
	return out
}

func moneyLine(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimSuffix(s, ".00")
	return strings.Replace(s, ".", ",", 1) + " ₽"
}

func (s *Server) vendorsActions(req actionRequest) (string, error) {
	switch req.Action {
	case "merge_vendor":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		name := strings.TrimSpace(req.Params["merge"])
		if name == "" || name == "—" {
			return "", nil
		}
		list, err := s.eng.Store().Vendors()
		if err != nil {
			return "", err
		}
		for _, v := range list {
			if v.Name == name && v.ID != id {
				// сливаем выбранного В того, на чьей строке нажали: имя
				// строки человек видит перед собой, оно и остаётся
				return vendorsScreen, s.eng.Store().MergeVendors(id, v.ID)
			}
		}
		return "", fmt.Errorf("поставщика «%s» нет", name)
	case "rename_vendor":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		name := strings.TrimSpace(req.Params["name"])
		if name == "" {
			return "", fmt.Errorf("имя не может быть пустым")
		}
		return vendorsScreen + "@" + req.Target, s.eng.Store().RenameVendor(id, name)
	}
	return "", errUnhandled
}
