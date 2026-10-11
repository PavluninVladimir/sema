package api

// «Настройки → Перевод». Отдельным разделом, а не строчкой в «Почте»:
// перевод — это отправка текста письма модели, и человек должен видеть
// рядом три вещи сразу — включено ли, на какой язык и КТО переводит.
// Последнее важнее прочего: локальная модель переводит бесплатно и никуда
// не отдаёт, облачная — наоборот.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

const translateScreen = "settings:translate"

func (s *Server) buildTranslate(doc *Document) error {
	doc.Heading = sectionTitle[translateScreen]
	doc.Layout = "wide"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	e := s.eng
	язык := e.TranslateTo()
	role := e.Role(engine.RoleTranslate)

	doc.Subheading = translateByPhrase(s, язык, role)

	языки := e.TranslateLangs()
	if !hasLanguage(языки, язык) {
		языки = append([]string{язык}, языки...)
	}
	add(Component{Type: "form", ID: "translate", Title: "Как переводить", Fields: []Field{
		{Type: "toggle", Name: "on", Label: "Переводить письма сам",
			Hint:  "каждое письмо — вызов модели; выключено — переводы уже сделанные остаются и показываются",
			Value: boolValue(e.TranslateOn())},
		{Type: "picker", Name: "to", Label: "На какой язык", Value: язык, Options: языки},
		{Type: "text", Name: "other", Label: "Другой язык",
			Hint: "напишите его — он встанет в список и будет выбран"},
		{Type: "text", Name: "chars", Label: "Сколько знаков переводить", Value: strconv.Itoa(e.TranslateChars()),
			Hint: "у ответа модели свой предел: письмо длиннее переводится с начала, и об этом сказано в самом переводе"},
	}, Submit: &Action{Action: "save_translate", Label: "Сохранить", Style: "primary"}})

	// счётчики: без них человек не знает, работает перевод или только включён
	сделано := e.Store().TranslationStats(язык)
	ждут, _ := e.Store().MessagesToTranslate(язык, 500)
	r := Component{Type: "row", Layout: "wide", Subtitle: "Переведено на «" + язык + "»",
		Caption: fmt.Sprintf("%s · ждут перевода: %d", plural(сделано, "письмо", "письма", "писем"), len(ждут))}
	if len(ждут) > 0 && e.TranslateOn() {
		r.Actions = []Action{{Action: "translate_backlog", Label: "Перевести накопившееся"}}
	}
	add(Component{Type: "section", Title: "Сколько сделано", Children: []Component{r}})

	add(Component{Type: "text", Style: "secondary", Text: "Подлинник письма не меняется: перевод лежит рядом отдельной записью, а поиск, память и разбор по-прежнему идут по тому, что прислали.\n" +
		"Секретное письмо уходит только доверенной модели, а нет такой — не уходит вовсе и ждёт. Закрытый ящик — так же.\n" +
		"Кто переводит — роль «Перевод» на экране «Модели и роли». Поставьте туда локальную модель: перевод станет бесплатным и не уйдёт наружу."})
	return nil
}

// translateByPhrase — подпись экрана: включено ли и кто переводит. Модель
// называем прямо: от неё зависит и цена, и то, уходит ли письмо наружу.
func translateByPhrase(s *Server, язык string, role engine.Role) string {
	if !s.eng.TranslateOn() {
		return "Выключен. Письма не на «" + язык + "» показываются как есть."
	}
	чем := role.Model
	if чем == "" || чем == "нет" {
		return "Включён, но роли «Перевод» не назначена модель — переводить некому."
	}
	куда := "не уходит наружу"
	if s.eng.ModelIsRemote(чем) {
		куда = "текст письма уходит наружу"
	}
	return "Переводит на «" + язык + "» · " + чем + " · " + куда + "."
}

func hasLanguage(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

func (s *Server) translateActions(req actionRequest) (string, error) {
	switch req.Action {
	case "save_translate":
		if v, ok := req.Params["on"]; ok {
			if err := s.eng.SetTranslateOn(v == "true"); err != nil {
				return "", err
			}
		}
		// «другой язык» весомее выпадашки: человек написал его руками
		язык := strings.TrimSpace(req.Params["other"])
		if язык == "" {
			язык = strings.TrimSpace(req.Params["to"])
		}
		if язык != "" {
			if err := s.eng.SetTranslateTo(язык); err != nil {
				return "", err
			}
		}
		if v := strings.TrimSpace(req.Params["chars"]); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return "", fmt.Errorf("«%s» — не число знаков", v)
			}
			if err := s.eng.SetTranslateChars(n); err != nil {
				return "", err
			}
		}
		return translateScreen, nil
	case "translate_backlog":
		n := s.eng.EnqueueTranslateBacklog(500)
		if n == 0 {
			return "", fmt.Errorf("переводить нечего: всё уже в очереди или переведено")
		}
		return translateScreen, nil
	}
	return "", errUnhandled
}
