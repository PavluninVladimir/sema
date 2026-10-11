package api

// «Подключить провайдера» — свой экран, а не форма посреди ленты: обязательное
// строками, остальное по требованию (приём 8 из `design/nastroyki-2/README.md`).
//
// Путь короткий: провайдер и ключ — и все его модели сразу оказываются в
// подключённых (имя роли — короткое имя модели, полное уходит в запрос). Дальше
// второй шаг бывает только там, где списка нет: клиент по подписке (ключ не
// нужен, спрашивается имя) и провайдер без списка — свой шлюз, Perplexity в
// режиме без каталога (название модели пишется руками). Состояние шага — в
// памяти процесса (`catalogState`), ключ в базу уходит зашифрованным, когда
// человек нажимает «Подключить».

import "fmt"

// buildProviderNew — экран подключения.
func (s *Server) buildProviderNew(doc *Document) error {
	doc.Heading = "Подключить провайдера"
	doc.Subheading = "Ключ подошёл и модели пришли — все они сразу оказываются в подключённых; имя роли — короткое имя модели"
	doc.Layout = "read"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	add(Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: modelsScreen, Label: "‹ Модели и роли"}}})

	s.mu.Lock()
	cat := s.catalog
	s.mu.Unlock()
	p, known := s.eng.Provider(cat.Provider)

	switch {
	case known && p.Kind == "cli":
		// у клиента по подписке ни ключа, ни списка моделей — он сам знает,
		// чем отвечать; спрашиваем только имя
		add(Component{Type: "form", ID: "addcli", Style: formRows, Title: "Подключить по подписке · " + p.Name,
			Fields: []Field{{Type: "text", Name: "name", Label: "Как назвать",
				Hint: p.Hint + " · ключ не нужен, оплата идёт подпиской"}},
			Submit: &Action{Action: "add_cli_model", Label: "Подключить", Style: "primary"}})
		add(Component{Type: "button", Actions: []Action{{Action: "reset_catalog", Label: "Выбрать другого провайдера"}}})
		return nil
	case known && !p.Catalog && cat.Provider != "":
		// провайдер списка не отдаёт (свой шлюз) — название модели человек пишет
		// сам; без этой формы шаг упирался в тупик
		add(Component{Type: "form", ID: "addmanual", Style: formRows, Title: "Подключить модель · " + p.Name,
			Fields: []Field{
				{Type: "text", Name: "name", Label: "Как назвать", Hint: "коротко: этим именем модель видна в ролях"},
				{Type: "text", Name: "model", Label: "Название модели", Hint: p.Hint},
				{Type: "secure", Name: "key", Label: "Ключ",
					Hint: "Сохранится зашифрованным. Посмотреть его потом нельзя — только заменить"},
			},
			Submit: &Action{Action: "add_model_manual", Label: "Подключить", Style: "primary"}})
		add(Component{Type: "button", Actions: []Action{{Action: "reset_catalog", Label: "Выбрать другого провайдера"}}})
		return nil
	case len(cat.Models) > 0:
		// список пришёл, а подключить всё разом не вышло: выбрать одну модель
		options := make([]string, 0, len(cat.Models))
		for _, m := range cat.Models {
			options = append(options, m.Title())
		}
		add(Component{Type: "form", ID: "pickmodel", Style: formRows, Title: "Подключить модель · " + cat.Provider,
			Fields: []Field{
				{Type: "text", Name: "name", Label: "Как назвать", Hint: "коротко: этим именем модель видна в ролях"},
				{Type: "picker", Name: "model", Label: "Модель",
					Hint:  fmt.Sprintf("список пришёл от провайдера · %d моделей", len(cat.Models)),
					Value: options[0], Options: options},
				{Type: "toggle", Name: "trusted", Label: "Доверять закрытое", Value: "false",
					Hint: "только сервер на своём железе или в своей сети: доверенная модель видит закрытые ящики и секретное"},
			},
			Submit: &Action{Action: "add_model", Label: "Добавить", Style: "primary"}})
		all := Action{Action: "add_all_models", Label: fmt.Sprintf("Подключить все · %d", len(cat.Models))}
		if len(cat.Models) > 20 {
			all.Confirm = fmt.Sprintf("Подключить все %d моделей провайдера «%s»? Список ролей станет длинным.", len(cat.Models), cat.Provider)
			all.ConfirmOK = "Подключить все"
		}
		add(Component{Type: "button", Actions: []Action{all, {Action: "reset_catalog", Label: "Выбрать другого провайдера"}}})
		return nil
	}

	// шаг первый: выбрать провайдера и ввести ключ
	hint := "адрес подставится сам — вписывать не нужно"
	if known && p.Hint != "" {
		hint = p.Hint
	}
	add(Component{Type: "form", ID: "connect", Style: formRows, Fields: []Field{
		{Type: "picker", Name: "provider", Label: "Провайдер", Hint: hint,
			Value: cat.Provider, Options: s.eng.ProviderNamesList()},
		{Type: "secure", Name: "key", Label: "Ключ",
			Hint: "сохранится зашифрованным; посмотреть его потом нельзя — только заменить"},
	}, Submit: &Action{Action: "load_catalog", Label: "Подключить и получить список", Style: "primary"}})
	if cat.Err != "" {
		add(Component{Type: "text", Style: "secondary", Text: "Провайдер: " + cat.Err})
	}
	// доверие пачкой не выдаётся: оно утверждение человека про конкретную модель
	add(Component{Type: "text", Style: "secondary", ID: "connect-trust",
		Text: "Модели подключаются внешними: получают только обычное. Доверие выдаётся каждой модели отдельно на её экране."})
	add(Component{Type: "text", Style: "secondary",
		Text: "Провайдер без списка (свой шлюз, подписка) ведёт ко второму шагу, где название модели пишется руками. " +
			"fal.ai рисует картинки к постам: ему нужен только ключ, самую дешёвую модель Сёма подберёт сам."})
	return nil
}
