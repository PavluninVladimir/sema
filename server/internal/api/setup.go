package api

// Мастер первого запуска. Пустая система — это не поломка: сервер поднялся,
// задачи заведены и ждут, а человеку показывают два шага — ящик и модель.
// Экран собирается теми же компонентами, что и остальные: клиент по-прежнему
// ничего не знает о предметной области.

import (
	"context"
	"fmt"
	"strconv"
)

func (s *Server) buildSetup(doc *Document) error {
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	hasMail, hasModel := s.eng.Configured()

	doc.Heading = "Здравствуйте. Давайте познакомимся"
	doc.Subheading = "Два шага — и Сёма начнёт разбирать почту сам. Всё, что вы сейчас выберете, " +
		"потом меняется в настройках, так что ошибиться тут нечем."

	// Шаг мастера: сделанный — галочкой, раскрытый — номером на синем,
	// будущий — серым. Три состояния видны разом, и человеку не нужно
	// гадать, где он стоит и сколько осталось. Раньше все шаги были
	// одинаковыми строками с капсулой «сейчас» или «готово», а поля
	// подключения лежали общим куском ниже — вне своего шага.
	шаг := func(n int, name, итог, состояние string, children ...Component) Component {
		return Component{Type: "step", ID: "setup-" + strconv.Itoa(n), Style: состояние,
			Text: strconv.Itoa(n), Subtitle: name, Caption: итог, Children: children}
	}

	// ——— шаг 1: почта ———
	if !hasMail {
		var внутри []Component
		внутри = append(внутри, Component{Type: "text", Style: "secondary",
			Text: "Сёма читает почту по IMAP: раскладывает входящие, судит спам и достаёт из писем чеки. " +
				"Ящиков может быть несколько, первый подключается здесь."})
		newMailbox(func(c Component) { внутри = append(внутри, c) })
		внутри = append(внутри, Component{Type: "text", Style: "secondary",
			Text: "Для Gmail вход делается один раз из терминала: go run ./cmd/sema -google-login — " +
				"команда откроет браузер и положит токен в базу. Остальные ящики подключаются прямо здесь, паролем приложения."})
		add(шаг(1, "Почта", "какие письма Сёма будет разбирать", "open", внутри...))
		add(шаг(2, "Модель", "кто будет думать за Сёму", "next"))
		add(setupTail())
		return nil
	}
	add(шаг(1, "Почта", mailResult(s), "done"))

	// ——— шаг 2: модель ———
	if !hasModel {
		var внутри []Component
		внутри = append(внутри, Component{Type: "text", Style: "secondary",
			Text: "Без модели Сёма умеет только складывать письма в стопку: решать, спам ли это, " +
				"читать чеки и отвечать вам — работа модели."})
		if url := s.eng.LocalModelServer(context.Background()); url != "" {
			внутри = append(внутри, Component{Type: "text", Style: "secondary",
				Text: "На этой машине уже поднят сервер моделей: " + url + " — такая модель сразу доверенная: " +
					"она на вашем железе, значит ей можно и закрытые ящики, и секретное. И денег не стоит."})
			внутри = append(внутри, Component{Type: "button", Actions: []Action{
				{Action: "quick_local_model", Label: "Подключить локальную модель", Style: "primary"},
			}})
		}
		внутри = append(внутри, Component{Type: "text", Style: "secondary",
			Text: "Или возьмите облачную: выберите провайдера в настройках и вставьте ключ. " +
				"Ключ подошёл — Сёма заберёт весь список моделей провайдера разом, выбирать по одной не придётся. " +
				"Облачная модель заводится внешней: закрытые ящики и секретное ей не показываются, пока вы не решите иначе."})
		внутри = append(внутри, Component{Type: "button", Actions: []Action{
			{Action: "$goto", Target: "settings", Label: "Открыть настройки моделей"},
		}})
		add(шаг(2, "Модель", "кто будет думать за Сёму", "open", внутри...))
		add(setupTail())
		return nil
	}
	add(шаг(2, "Модель", "отвечает "+s.eng.ActiveProvider(), "done"))

	add(Component{Type: "text", Style: "secondary",
		Text: "Всё готово: фоновые задачи разбирают почту. Этот экран больше не появится."})
	add(Component{Type: "button", Actions: []Action{
		{Action: "$goto", Target: "inbox", Label: "К почте", Style: "primary"},
	}})
	return nil
}

// mailResult — что показать в сделанном шаге вместо обещания: какой ящик
// подключён на самом деле.
func mailResult(s *Server) string {
	accounts := s.eng.Accounts()
	if len(accounts) == 0 {
		return "ящик подключён"
	}
	if len(accounts) > 1 {
		return fmt.Sprintf("%s и ещё %d", accounts[0].Username, len(accounts)-1)
	}
	return accounts[0].Username
}

// setupTail — пустой экран после первого запуска пугает: кажется, что
// ничего не работает. Говорим прямо, что происходит прямо сейчас.
func setupTail() Component {
	return Component{Type: "text", Style: "secondary",
		Text: "Пока вы читаете, Сёма уже работает: задачи заведены и крутятся, просто им нечего разбирать. " +
			"Пустой экран после первого запуска — это не поломка, а ожидание."}
}
