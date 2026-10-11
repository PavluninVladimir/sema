package api

// «Добавить секрет» — свой экран `settings:secrets@new`, а не форма под списком:
// форма из трёх полей, развёрнутая всегда, стояла под двадцатью семью строками.
// Вид, имя, значение и заметка; «Сгенерировать ключ SSH» — там же: вид
// «Ключ SSH — сгенерировать» создаёт пару ed25519 сам, приватная часть остаётся
// закрытой в базе, а публичную показывает экран нового секрета. Приватный ключ для
// импорта — отдельное поле под «Ещё 1 поле»: он многострочный, а значение токена
// или пароля — поле `secure`, которое браузер не показывает.
//
// Что вписано — то и уйдёт в `add_secret`: имена полей `kind`, `name` и `value`
// прежние, `private` и `note` добавились.

import "strings"

func (s *Server) buildSecretNew(doc *Document, tail string) error {
	st := s.eng.Store()
	doc.Heading = "Добавить секрет"
	doc.Subheading = "Значение закрывается на входе и больше не показывается; модуль получает его по имени, если вы выдадите"
	doc.Layout = "read"
	crumb := Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: secretsScreen, Label: "‹ Ключи и секреты"}}}
	if !st.SecretsEncrypted() {
		doc.Components = append(doc.Components, crumb, Component{Type: "section", ID: "secret-new-no-key", Children: []Component{{
			Type: "row", Layout: "wide", Level: LevelAlert, Dot: levelDot(LevelAlert), Subtitle: "Ключ шифрования не задан",
			Caption: "Секреты кладутся в базу только закрытыми. Запустите сервер с переменной " + s.eng.KeyEnvName() +
				" — и форма появится",
			Actions: []Action{{Action: "$goto", Target: entityAddr(secretsScreen, "", secretsTabCrypt), Label: "Шифрование", Style: "primary"}}}}})
		return nil
	}
	name, _ := strings.CutPrefix(tail, secretsNewName)
	// пришли с вписанным именем — секрет просит модуль (поручение, карточка
	// «Нужен секрет»), и это пароль или токен, а не новый ключ SSH: с видом
	// «сгенерировать» поля значения не видно, и 4 октября 2026 владелец не
	// нашёл, куда вписать логин
	kind := secretKinds[0]
	if strings.TrimSpace(name) != "" {
		kind = secretKindPassword
	}
	doc.Components = append(doc.Components, crumb, Component{Type: "form", ID: "secret-new", Style: formRows,
		Fields: []Field{
			{Type: "picker", Name: "kind", Label: "Вид", Options: secretKinds, Value: kind,
				Hint: "Для ключа SSH Сёма сам создаст пару ed25519: приватная часть останется закрытой в базе, публичную вы добавите на сервер"},
			{Type: "text", Name: "name", Label: "Имя", Value: strings.TrimSpace(name),
				Hint: "например ssh:db-2 или gitlab-token · по этому имени модуль просит секрет; имя «new» занято"},
			{Type: "secure", Name: "value", Label: "Значение",
				Hint: "токен, пароль или произвольный секрет · при генерации ключа SSH оставьте пустым"},
			{Type: "text", Name: "note", Label: "Заметка",
				Hint: "необязательно: для чего он — видно в списке и на его экране"},
			{Type: "code", Name: "private", Label: "Приватный ключ для импорта", More: true,
				Hint: "только для вида «Ключ SSH — импортировать»: -----BEGIN … PRIVATE KEY-----"},
		},
		Submit: &Action{Action: "add_secret", Label: "Сохранить", Style: "primary"}})
	return nil
}
