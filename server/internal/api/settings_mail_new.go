package api

// «Новый ящик» — свой экран `settings:mail@new`, а не форма под списком:
// форма из пяти полей, развёрнутая под списком ящиков, была больше самого
// списка. Обязательных четыре — назначение, сервис, адрес и пароль; хост нужен
// только «Другому IMAP» и свёрнут в той же форме под «Ещё N полей»
// (`Field.More`): у формы с одной кнопкой «Подключить» близнец из «Тонкой
// настройки» потерял бы отправку.
//
// Что вписано — то и уйдёт в `add_account`: имена полей прежние. Та же форма
// стоит в мастере первого запуска (setup.go).

// Сервисы для подключения: подпись → пресет для AddAccount.
var mailPresets = [][2]string{
	{"Яндекс", "yandex"}, {"Mail.ru", "mailru"}, {"Gmail", "gmail"}, {"Exchange / Office 365", "exchange"}, {"Другой IMAP", "imap"},
}

func mailPresetFor(title string) string {
	for _, p := range mailPresets {
		if p[0] == title || p[1] == title {
			return p[1]
		}
	}
	return "imap"
}

// buildMailNew — экран «Новый ящик». Пути назад нет, пока нет ни одного
// ящика: «‹ Почта» вёл бы сюда же.
func (s *Server) buildMailNew(doc *Document, hasBoxes bool) error {
	doc.Heading = "Новый ящик"
	doc.Subheading = "Сёма войдёт, прочитает список папок с сервера и покажет, что с какой делает"
	doc.Layout = "read"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	if hasBoxes {
		add(Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: mailScreen, Label: "‹ Почта"}}})
	}
	newMailbox(add)
	return nil
}

// newMailbox — форма и пояснение под ней. Зовётся и экраном «Новый ящик», и
// мастером первого запуска.
func newMailbox(add func(Component)) {
	var services []string
	for _, p := range mailPresets {
		services = append(services, p[0])
	}
	add(Component{
		Type:  "form",
		ID:    "addaccount",
		Style: formRows,
		Fields: []Field{
			{Type: "segments", Name: "purpose", Label: "Ящик", Value: "личный", Options: titlesOf(purposeTitles),
				Hint: "рабочий по умолчанию закрыт для облачной модели и не даёт чеков; остальное настраивается на экране ящика после подключения"},
			{Type: "picker", Name: "preset", Label: "Сервис", Value: services[0], Options: services,
				Hint: "Яндекс и Mail.ru — пароль приложения; Gmail — вход через Google после добавления; Exchange — Office 365; другой IMAP — укажите сервер"},
			{Type: "text", Name: "email", Label: "Адрес ящика", Hint: "под ним Сёма входит"},
			{Type: "secure", Name: "password", Label: "Пароль приложения", Hint: "не основной пароль от аккаунта"},
			{Type: "text", Name: "host", Label: "IMAP-сервер", More: true,
				Hint: "только для «Другой IMAP», порт 993, шифрование обязательно"},
		},
		Submit: &Action{Action: "add_account", Label: "Подключить", Style: "primary"},
	})
	add(Component{Type: "text", Style: "secondary",
		Text: "После подключения Сёма прочитает список папок с сервера, и ящик появится в списке «Ящики»: на его экране задаются папки, архив, спам и задачи."})
}
