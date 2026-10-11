package api

// «Добавить сервер MCP» — свой экран `settings:integrations@new`, а не форма
// под списком: десять полей, развёрнутые всегда, были больше самого списка.
// Обязательных три — имя, адрес и ключ; остальное нужно редко и свёрнуто в той
// же форме под «Ещё N полей» (`Field.More`): у формы с одной кнопкой
// «Добавить и проверить» близнец из «Тонкой настройки» потерял бы отправку.
//
// Что вписано — то и уйдёт в `add_mcp`: имена полей прежние.

func (s *Server) buildMCPNew(doc *Document) error {
	doc.Heading = "Добавить сервер MCP"
	doc.Subheading = "Ключ или токен входа подставляет ядро, в текст модели они не попадают; серверу у агента адрес не нужен"
	doc.Layout = "read"
	doc.Components = append(doc.Components,
		Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: mcpScreen, Label: "‹ Интеграции"}}},
		Component{Type: "form", ID: "mcp-new", Style: formRows,
			Fields: []Field{
				{Type: "text", Name: "name", Label: "Имя", Hint: "Коротко и латиницей: planner. Адрес экрана строится из имени, поэтому без «@» и «~»"},
				{Type: "text", Name: "url", Label: "Адрес", Hint: "https://…/mcp; пусто, если сервер живёт у агента (поле «Агент» ниже)"},
				{Type: "text", Name: "secret", Label: "Имя секрета с токеном",
					Hint: "Из «Ключей и секретов». Пусто — без ключа: если сервер потребует вход OAuth, на экране сервера появится кнопка «Войти»"},
				{Type: "text", Name: "agent", Label: "Агент", More: true,
					Hint: "Имя агента с «Агентов»: тогда вызовы идут по шифрованному каналу, а не по HTTP, и сервер чату не открывается"},
				{Type: "text", Name: "can", Label: "Способность агента", More: true, Hint: "Только для сервера у агента; по умолчанию «код»"},
				{Type: "text", Name: "header", Label: "Заголовок", More: true, Hint: "В каком заголовке уходит ключ; по умолчанию Authorization"},
				{Type: "text", Name: "prefix", Label: "Приставка к значению", More: true, Hint: "Что писать перед ключом; по умолчанию «Bearer »"},
				{Type: "text", Name: "login_scope", Label: "Права входа", More: true,
					Hint: "Необязательно, если у сервера вход OAuth: права через пробел; пусто — все, что назовёт сервер"},
				{Type: "text", Name: "about", Label: "Что это", More: true, Hint: "Необязательно: одна строка для чата — о чём этот сервер"},
				{Type: "text", Name: "keywords", Label: "Когда звать", More: true,
					Hint: "Необязательно: слова через запятую; встретились в вопросе — чат получает полный список инструментов"},
			},
			Submit: &Action{Action: "add_mcp", Label: "Добавить и проверить", Style: "primary"}})
	return nil
}
