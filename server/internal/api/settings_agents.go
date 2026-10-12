package api

// Раздел «Агенты»: кто может звонить Сёме, чем он себя доказывает и что ему
// разрешено. Две вкладки — Агенты · Шлюз; «Тонкая настройка» (где слушать
// агентов, адрес шлюза и куда он вносит запросы) внизу той вкладки, к которой
// относится; журнал — последним, ленивым. У каждого агента свой экран
// (settings_agent.go), а приглашение — свой экран (settings_agent_new.go).
//
// До 2 октября 2026 это была одна лента на 4,1 экрана: агенты с раскрытием и
// вложенными «Способностями», абзац, «Кто с кем разговаривает» с корневым
// сертификатом, форма «Позвать агента» (развёрнута всегда), журнал на пятнадцать
// строк, потом «Вход из интернета» и две формы. Четыре дела на одном экране:
// агенты, приглашение, журнал, шлюз. Теперь:
//
//   - «Агенты» — тонкие строки (имя, связь, не больше двух капсул, одна кнопка,
//     «···»), сверху полка «Ждут вас»: сменившийся отпечаток и агент, у которого
//     ни одна способность не разрешена;
//   - «Пригласить агента» — одна кнопка шапки, а приглашение — свой экран: код
//     виден там один раз;
//   - «Шлюз» — связь со шлюзом, что он пропускает, предупреждения;
//   - корневой сертификат удостоверяющего центра — карточка с отпечатком, а не
//     строка общего списка: закрытая часть не показывается никогда.
//
// Правила агентов не ослаблены: способность разрешает человек поимённо и
// неразрешённую Сёма не зовёт вовсе (`AskAgent`); отзыв действует сразу; войти
// агент может только одноразовым кодом. Тумблера «разрешить всё» нет.
//
// Адреса:
//
//	settings:agents                    раздел, вкладка «Агенты»
//	settings:agents~gateway            «Шлюз»
//	settings:agents@new                «Пригласить агента»
//	settings:agents@<имя>              экран агента, «Обзор»
//	settings:agents@<имя>~caps|log

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const agentsScreen = "settings:agents"

// Внутренние вкладки. У раздела две: Агенты и Шлюз; у агента три: Обзор,
// Способности, Журнал.
const (
	agentsTabList    = ""        // раздел: Агенты; агент: Обзор
	agentsTabGateway = "gateway" // раздел: Шлюз
	agentTabCaps     = "caps"    // агент: Способности
	agentsTabLog     = "log"     // агент: Журнал

	// agentsNew — «агент» с этим именем означает экран приглашения
	agentsNew = "new"
)

// agentsLogPart — часть экрана с журналом всех агентов (реестр parts.go).
const agentsLogPart = "agents-log"

// agentsLogPage — сколько строк журнала приезжает первой страницей.
const agentsLogPage = 20

// agentsAddr — разобранный адрес экрана «Агентов».
type agentsAddr struct {
	Name string // имя агента; пусто — раздел; «new» — «Пригласить агента»
	Tab  string // вкладка
}

func parseAgentsAddr(screen string) agentsAddr {
	_, id, tab := splitEntity(screen)
	return agentsAddr{Name: strings.TrimSpace(id), Tab: tab}
}

// agentCheck — результат последней «Проверить связь»: чей и что сказал.
type agentCheck struct{ Name, Text string }

// freshInvite — только что выданное приглашение: код живёт в памяти ровно до
// первого показа. Дальше экран знает приглашение по отпечатку (`Invite.Ref`), а
// сам код — нет: так он не лежит ни в разметке, ни в закладках, ни в снимке.
type freshInvite struct {
	Name, Role, Code, Until string
}

func (s *Server) buildAgents(doc *Document, screen, filter string, limit int) error {
	a := parseAgentsAddr(screen)
	doc.Layout = "wide"
	switch {
	case a.Name == agentsNew:
		return s.buildAgentNew(doc)
	case a.Name != "":
		return s.buildAgent(doc, a, filter, limit)
	}
	return s.buildAgentsSection(doc, a.Tab)
}

// agentLinks — кто сейчас на связи и сколько: имя → «3 ч». Живые соединения
// спрашиваются один раз на экран, а не по разу на агента.
// agentLinksFor — живые провода для экрана. Экран, который собирает индекс
// настроек, живых проводов не знает: «на связи» оттуда выглядело бы вопросом жителю
// машины (цифры экономии, ответ шлюза), а индексу наружу ходить нельзя.
func (s *Server) agentLinksFor(doc *Document) map[string]string {
	if doc.offline {
		doc.partial = true
		return map[string]string{}
	}
	return s.agentLinks()
}

func (s *Server) agentLinks() map[string]string {
	out := map[string]string{}
	list := s.eng.AgentLinks()
	if s.linksVia != nil {
		list = s.linksVia() // в тестах живых проводов нет: их называет проба
	}
	for _, l := range list {
		name, rest, ok := strings.Cut(l, " · на связи ")
		if !ok {
			continue
		}
		// движок называет срок по-го́шному («3h12m0s»), человеку — «3 ч 12 мин»
		if d, err := time.ParseDuration(rest); err == nil {
			rest = holdWords(d)
		}
		out[name] = rest
	}
	return out
}

// holdWords — как долго держится связь, словами: «меньше минуты», «42 мин»,
// «3 ч 12 мин», «2 дн. 5 ч».
func holdWords(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	switch {
	case d < time.Minute:
		return "меньше минуты"
	case minutes < 60:
		return strconv.Itoa(minutes) + " мин"
	case minutes < 24*60:
		if minutes%60 == 0 {
			return strconv.Itoa(minutes/60) + " ч"
		}
		return strconv.Itoa(minutes/60) + " ч " + strconv.Itoa(minutes%60) + " мин"
	}
	days, hours := minutes/(24*60), minutes%(24*60)/60
	if hours == 0 {
		return strconv.Itoa(days) + " дн."
	}
	return strconv.Itoa(days) + " дн. " + strconv.Itoa(hours) + " ч"
}

// agentStatePill — как агент себя чувствует, одним словом. Норма (на связи)
// тоже названа: у агента, который спит по ночам, «не на связи» — не поломка, но и
// не то же, что «на связи».
func agentStatePill(a *store.Agent, links map[string]string) Badge {
	switch {
	case a.State == store.AgentRevoked:
		return Badge{Text: "доступ отозван"}
	case a.State == store.AgentFPBad:
		return Badge{Text: "отпечаток сменился", Level: LevelAlert}
	}
	if since, ok := links[a.Name]; ok {
		return Badge{Text: "на связи · " + since, Level: LevelOK}
	}
	if a.Seen == "" {
		return Badge{Text: "ещё не звонил", Level: LevelInfo}
	}
	return Badge{Text: "не на связи", Level: LevelWarn}
}

// agentTile — буква и цвет плитки: шлюз отличается от жителя цветом.
func agentTile(a *store.Agent) (letter, color string) {
	letter, color = firstRuneOfName(a.Name), "#2E7DD1"
	if engine.AgentRole(a) == engine.RoleGateway {
		color = "#17181C"
	}
	return letter, color
}

// agentCaps — сколько способностей разрешено из объявленных и какие не разрешены.
func agentCaps(a *store.Agent) (allowed, declared int, waiting []string) {
	on := map[string]bool{}
	for _, c := range a.Allowed {
		on[c] = true
	}
	for _, c := range a.Can {
		declared++
		if on[c] {
			allowed++
		} else {
			waiting = append(waiting, c)
		}
	}
	return allowed, declared, waiting
}

// agentCaption — одна строка фактов шапки и строки списка: роль, связь,
// способности.
func agentCaption(a *store.Agent) string {
	parts := []string{"роль «" + engine.AgentRole(a) + "»"}
	switch {
	case a.State == store.AgentFPBad:
		parts = append(parts, "соединения отклоняются, пока вы не приняли новый отпечаток")
	case a.State == store.AgentRevoked:
		parts = append(parts, "сертификат остался на руках, но Сёма его не признаёт")
	case a.Seen == "":
		parts = append(parts, "ещё ни разу не звонил")
	default:
		parts = append(parts, "был на связи "+whenText(a.Seen))
	}
	if a.State == store.AgentOK {
		allowed, declared, _ := agentCaps(a)
		if declared > 0 {
			parts = append(parts, fmt.Sprintf("способностей разрешено %d из %d", allowed, declared))
		}
	}
	return strings.Join(parts, " · ")
}

// agentPrimary — единственная кнопка строки и шапки агента: то, что сейчас
// нужно сделать. Сменился отпечаток — принять или тревожиться; доступ отозван —
// новое приглашение; иначе — проверить связь, если у агента разрешён «код»
// (проверка зовёт именно его, и без него она всегда отвечала бы отказом).
func (s *Server) agentPrimary(a *store.Agent) *Action {
	switch {
	case a.State == store.AgentFPBad:
		act := acceptAgentFPAction(a)
		act.Style = "primary"
		return &act
	case a.State == store.AgentRevoked:
		return &Action{Action: "invite_agent", Target: a.Name, Label: "Новое приглашение", Style: "primary"}
	case a.State == store.AgentOK && s.eng.AgentAllowed(a.Name, "код"):
		return &Action{Action: "check_agent", Target: a.Name, Label: "Проверить связь"}
	}
	return nil
}

// acceptAgentFPAction — «Принять новый отпечаток…» с вопросом: так бывает после
// переустановки агента, а если его не переустанавливали — это тревога.
func acceptAgentFPAction(a *store.Agent) Action {
	return Action{Action: "accept_agent_fp", Target: a.Name, Label: "Принять новый отпечаток…", ConfirmOK: "Принять",
		Confirm: "Принять новый отпечаток агента «" + a.Name + "»? Так бывает после переустановки; если вы его не переустанавливали — это тревога."}
}

func revokeAgentAction(a *store.Agent) Action {
	return Action{Action: "revoke_agent", Target: a.Name, Label: "Отозвать доступ…", Style: "destructive", ConfirmOK: "Отозвать",
		Confirm: "Отозвать доступ агента «" + a.Name + "»? Он перестанет отвечать сразу, а разрешённые способности пропадут."}
}

func forgetAgentAction(a *store.Agent) Action {
	return Action{Action: "forget_agent", Target: a.Name, Label: "Забыть…", Style: "destructive", ConfirmOK: "Забыть",
		Confirm: "Забыть агента «" + a.Name + "»? Его сертификат перестанет признаваться, а чтобы вернуть, нужно новое приглашение."}
}

// agentRow — тонкая строка списка: имя, не больше двух капсул (связь и, если
// отличает, «шлюз»), одна фраза, одна кнопка, «···». Вся строка ведёт на экран
// агента. Способности, отпечаток и журнал — на самом экране.
func (s *Server) agentRow(a *store.Agent, links map[string]string) Component {
	letter, color := agentTile(a)
	pills := []Badge{agentStatePill(a, links)}
	if engine.AgentRole(a) == engine.RoleGateway {
		pills = append(pills, Badge{Text: "шлюз"})
	}
	row := Component{Type: "row", Layout: "wide", ID: "agent-" + a.Name, Subtitle: a.Name,
		Avatar: letter, AvatarColor: color, AvatarShape: "square",
		Caption: agentCaption(a), Badges: pills}
	if a.State == store.AgentFPBad {
		withLevel(&row, LevelAlert)
	}
	row.Actions = []Action{{Action: "$goto", Target: entityAddr(agentsScreen, a.Name, ""), Label: a.Name}}
	primary := s.agentPrimary(a)
	if primary != nil {
		row.Actions = append(row.Actions, *primary)
	}
	// «···»: что не главное. Путь по вкладкам короче, чем вспоминать, где что
	if a.State == store.AgentFPBad && (primary == nil || primary.Action != "accept_agent_fp") {
		row.Menu = append(row.Menu, acceptAgentFPAction(a))
	}
	row.Menu = append(row.Menu,
		Action{Action: "$goto", Target: entityAddr(agentsScreen, a.Name, agentTabCaps), Label: "Способности"},
		Action{Action: "$goto", Target: entityAddr(agentsScreen, a.Name, agentsTabLog), Label: "Журнал"})
	if primary == nil || primary.Action != "invite_agent" {
		row.Menu = append(row.Menu, Action{Action: "invite_agent", Target: a.Name, Label: "Новое приглашение"})
	}
	if a.State != store.AgentRevoked {
		row.Menu = append(row.Menu, revokeAgentAction(a))
	}
	row.Menu = append(row.Menu, forgetAgentAction(a))
	return row
}

// agentsShelf — полка «Ждут вас»: сменившийся отпечаток и агент, у которого ни
// одна объявленная способность не разрешена. Всё, что не заработает без
// человека, одним тёплым блоком; нет ничего — полки нет.
func (s *Server) agentsShelf(agents []*store.Agent) *Component {
	var rows []Component
	for _, a := range agents {
		switch {
		case a.State == store.AgentFPBad:
			rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-wait-fp-" + a.Name, Level: LevelAlert, Dot: levelDot(LevelAlert),
				Subtitle: "У «" + a.Name + "» сменился отпечаток",
				Caption:  "Соединения отклоняются, пока вы не решите. Так бывает после переустановки агента; если вы его не переустанавливали — это тревога",
				Facts:    []Prop{{Label: "Было", Value: a.Fingerprint}, {Label: "Стало", Value: a.NewFP, Color: "#C4271C"}},
				Actions:  []Action{{Action: "$goto", Target: entityAddr(agentsScreen, a.Name, ""), Label: "Посмотреть", Style: "primary"}},
				Menu:     []Action{acceptAgentFPAction(a), revokeAgentAction(a)}})
		case a.State == store.AgentOK:
			allowed, declared, waiting := agentCaps(a)
			if declared == 0 || allowed > 0 {
				continue
			}
			rows = append(rows, Component{Type: "row", Layout: "wide", ID: "agent-wait-caps-" + a.Name, Level: LevelWarn, Dot: levelDot(LevelWarn),
				Subtitle: "«" + a.Name + "» объявил способности, и ни одна не разрешена",
				Caption:  strings.Join(waiting, ", ") + " — пока вы не разрешили, Сёма их не зовёт вовсе",
				Actions:  []Action{{Action: "$goto", Target: entityAddr(agentsScreen, a.Name, agentTabCaps), Label: "Решить", Style: "primary"}}})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return &Component{Type: "shelf", ID: "agents-shelf", Title: "Ждут вас", Children: rows}
}

// agentsTabs — вкладки раздела. Счётчик у вкладки говорит, что там есть решать
// или чинить, до того как человек туда зайдёт.
func (s *Server) agentsTabs(agents []*store.Agent, links map[string]string) []innerTab {
	tabs := []innerTab{
		{ID: agentsTabList, Name: "Агенты", Count: strconv.Itoa(len(agents))},
		{ID: agentsTabGateway, Name: "Шлюз"},
	}
	waiting := 0
	for _, a := range agents {
		if a.State == store.AgentFPBad {
			waiting++
		}
	}
	if waiting > 0 {
		tabs[0].Count, tabs[0].Level = strconv.Itoa(waiting)+" ждут", LevelAlert
	}
	// шлюз заведён, а провода нет — или правила доступа не готовы к выходу
	// наружу: это видно с полосы вкладок
	if gw, has := s.eng.GatewayAgent(); has && s.eng.GatewayAddr() != "" {
		if _, live := links[gw.Name]; !live {
			tabs[1].Count, tabs[1].Level = "провода нет", LevelAlert
		} else if s.eng.GatewayWarning() != "" {
			tabs[1].Count, tabs[1].Level = "!", LevelWarn
		}
	}
	return tabs
}

// agentsPhrase — строка под заголовком раздела: сколько агентов и как они.
func agentsPhrase(agents []*store.Agent, links map[string]string) string {
	if len(agents) == 0 {
		return "Агент — житель вашей машины: он делает на месте то, чего Сёма не может издалека. Звонит он сам, разговор закрыт, и обе стороны предъявляют друг другу сертификат."
	}
	live := 0
	for _, a := range agents {
		if _, ok := links[a.Name]; ok {
			live++
		}
	}
	return plural(len(agents), "агент", "агента", "агентов") + ", на связи " + strconv.Itoa(live) +
		". Звонит агент сам, разговор закрыт, и обе стороны предъявляют друг другу сертификат."
}

func (s *Server) buildAgentsSection(doc *Document, tab string) error {
	st := s.eng.Store()
	doc.Heading = "Агенты"
	agents, err := st.Agents()
	if err != nil {
		return err
	}
	links := s.agentLinksFor(doc)
	doc.Subheading = agentsPhrase(agents, links)
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	setSubtabs(doc, agentsScreen, tab, s.agentsTabs(agents, links))
	// «Пригласить агента» — единственная кнопка шапки раздела и стоит на всех
	// вкладках: шапка не меняется от вкладки к вкладке
	doc.ToolbarActions = []Action{{Action: "$goto", Target: entityAddr(agentsScreen, agentsNew, ""),
		Label: "Пригласить агента", Style: "primary"}}

	if tab == agentsTabGateway {
		s.agentsGateway(add, links)
		return nil
	}

	// то, что ждёт вас, стоит вверху
	if shelf := s.agentsShelf(agents); shelf != nil {
		add(*shelf)
	}
	rows := make([]Component, 0, len(agents))
	for _, a := range agents {
		rows = append(rows, s.agentRow(a, links))
	}
	if len(rows) == 0 {
		empty := emptyState("Агентов пока нет",
			"Агент ставится на вашу машину и звонит Сёме сам: он умеет то, чего нельзя издалека — читать код языковыми серверами и показывать уведомления у часов. Позовите первого: он получит сертификат по одноразовому коду", "person")
		empty.Actions = []Action{{Action: "$goto", Target: entityAddr(agentsScreen, agentsNew, ""), Label: "Пригласить агента", Style: "primary"}}
		rows = append(rows, empty)
	}
	add(Component{Type: "section", ID: "agents-list", Title: "Агенты · " + strconv.Itoa(len(agents)), Children: rows})

	add(s.rootCard())

	add(Component{Type: "section", ID: "agents-how", Title: "Как это устроено", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary",
		Text: "Способности объявляет сам агент, а разрешаете вы — каждую отдельно, на экране агента. Новая появляется выключенной, и неразрешённую Сёма не зовёт вовсе. Отзыв действует сразу. " +
			"Агент входит один раз — одноразовым кодом, который живёт несколько минут и сгорает при первом же использовании; это единственная дверь без сертификата. " +
			"Дальше он звонит сам: ноутбук спит, меняет сеть и стоит за домашним роутером — стучаться к нему бесполезно. " +
			"Если у знакомого имени вдруг сменится отпечаток, Сёма не переподключится молча, а поднимет тревогу."}}})

	// где слушать агентов — «Тонкая настройка» (settings_rare.go): это заводят
	// один раз, а не правят
	where := s.eng.AgentListen()
	hint := "Например 0.0.0.0:8766; пусто — не принимать соединений из сети"
	if where == "" {
		hint += ". Сейчас не слушает: агенты подключаются только по петле"
	} else {
		hint += ". Сейчас слушает " + where
	}
	add(Component{Type: "form", ID: "agent-listen", Title: "Где слушать агентов", Tier: tierRare, Style: formRows,
		Fields: []Field{s.settingField("agents.listen", "listen", "Адрес", hint, "")},
		Submit: &Action{Action: "set_agent_listen", Label: "Сохранить"}})

	// когда говорить о пропаже — тоже «Тонкая настройка»: шлюз
	// переподключается за секунду, и пуш на каждый обрыв будил зря
	add(Component{Type: "form", ID: "agent-off-notify", Title: "Пропажа агента", Tier: tierRare, Style: formRows,
		Fields: []Field{s.settingField(engine.AgentOffNotifyKey, "off_notify", "Сказать, если агента нет дольше",
			"Агент обычно перезванивает за секунды, и такой обрыв вас не тревожит. 0 — говорить сразу о каждом", "с")},
		Submit: &Action{Action: "set_agent_off_notify", Label: "Сохранить"}})

	// журнал — хроника, а не настройка: последним и по раскрытию
	add(Component{Type: "section", ID: agentsLogPart, Title: "Журнал агентов", Caption: "о чём спрашивали, кто входил и что разрешали — по всем агентам",
		Tier: tierTail, Collapsed: true, Lazy: agentsLogPart})
	return nil
}

// rootCard — корневой сертификат удостоверяющего центра Сёмы: отпечаток виден, а
// закрытая часть лежит в хранилище секретов и не показывается никогда. Действий
// над корнем здесь нет: сменить его значит отвалить всех агентов разом, и такой
// кнопки на экране не было и не появилось.
func (s *Server) rootCard() Component {
	fp := s.eng.RootFingerprint()
	row := Component{Type: "row", Layout: "wide", ID: "agents-root", Subtitle: "Корневой сертификат"}
	if fp == "" {
		row.Caption = "Корень ещё не заведён — он появится, когда вы позовёте первого агента"
	} else {
		row.Facts = []Prop{{Label: "Отпечаток", Value: fp}}
		row.Caption = "Закрытая часть лежит в хранилище секретов и наружу не выдаётся. " +
			"Сменить или убрать корень отсюда нельзя: все агенты разом перестали бы признавать Сёму"
		row.Actions = []Action{{Action: "$copy", Target: fp, Label: "Скопировать отпечаток"}}
	}
	return Component{Type: "section", ID: "agents-ca", Title: "Удостоверяющий центр Сёмы", Children: []Component{row}}
}

// ---- «Шлюз» ----

// agentsGateway — вкладка «Шлюз»: край, который стоит снаружи, — связь, домен,
// сертификат, сколько отсеяно, — и предупреждения. Отказ шлюза отличим от отказа
// самого Сёмы: у каждой строки сказано, чей это конец провода. «Тонкая настройка»
// вкладки — публичный адрес шлюза и куда он вносит запросы.
func (s *Server) agentsGateway(add func(Component), links map[string]string) {
	addr := s.eng.GatewayAddr()
	agent, has := s.eng.GatewayAgent()
	var rows []Component

	if trouble := s.eng.GatewayWarning(); trouble != "" {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "gateway-warning", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "Правила доступа не готовы к выходу наружу", Caption: trouble,
			Actions: []Action{{Action: "$goto", Target: "settings:access", Label: "Открыть «Вход и доступ»", Style: "primary"}}})
	}

	switch {
	case !has && addr == "":
		rows = append(rows, emptyState("Шлюза нет",
			"Шлюз — тот же агент, но с постоянным адресом и доменом: он держит край в интернете и вносит запросы к Сёме по уже опознанному проводу. "+
				"Дома при этом не открывается ни один порт — соединение открывает Сёма сам. "+
				"Позовите агента, выбрав роль «шлюз», а потом впишите его адрес внизу, в «Тонкой настройке»", "globe"))
	case !has:
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "gateway-no-agent", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "Адрес шлюза задан, а самого шлюза нет",
			Caption:  "по адресу " + addr + " звонить некому: позовите агента роли «шлюз»",
			Actions:  []Action{{Action: "$goto", Target: entityAddr(agentsScreen, agentsNew, ""), Label: "Пригласить шлюз", Style: "primary"}}})
	case addr == "":
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "gateway-no-addr", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "Шлюз «" + agent.Name + "» заведён, а адрес не задан",
			Caption:  "звонит Сёма, и звонить ему пока некуда — впишите публичный адрес шлюза внизу, в «Тонкой настройке»"})
	default:
		rows = append(rows, s.gatewayRow(agent, addr, links))
	}
	add(Component{Type: "section", ID: "gateway-state", Title: "Вход из интернета", Children: rows})

	add(Component{Type: "form", ID: "gateway-addr", Title: "Куда звонить шлюзу", Tier: tierRare, Style: formRows,
		Fields: []Field{
			s.settingField("agents.gateway", "gateway", "Публичный адрес шлюза",
				"Например sema.example.org:8767 — один порт на всё: и управляющий провод, и каждый поток браузера. "+
					"Один нарочно: дома наружу открывается ровно одно направление, диапазон открывать не придётся. "+
					"Пусто — шлюза нет, и Сёма работает как прежде", ""),
			s.settingField("agents.gateway_upstream", "upstream", "Куда он вносит запросы внутри Сёмы",
				"Пусто — свой же порт по петле ("+s.eng.GatewayUpstream()+"); этот адрес наружу не ходит никогда", ""),
		},
		Submit: &Action{Action: "set_gateway", Label: "Сохранить"}})
	add(Component{Type: "section", ID: "gateway-how", Title: "Как это устроено", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary",
		Text: "Адрес шлюза не секрет: подставят чужой — рукопожатие не сойдётся по сертификату, и разговора не будет. " +
			"Отсев запросов на краю делает сам шлюз: он знает список путей Сёмы, и всё прочее до него не доходит вовсе."}}})
}

// gatewayRow — шлюз с проводом: наш конец провода (потоки, куда звоним) и его
// (домен, сертификат, сколько пропущено и отсеяно). Всё выше — наш конец, ниже —
// его, и он подписан как его: человек должен понимать, где чинить.
func (s *Server) gatewayRow(agent *store.Agent, addr string, links map[string]string) Component {
	level, pill := LevelOK, Badge{Text: "провод жив", Level: LevelOK}
	_, live := links[agent.Name]
	if !live {
		level, pill = LevelAlert, Badge{Text: "провода нет", Level: LevelAlert}
	}
	stats := s.eng.GatewayStats()
	props := []Prop{
		{Label: "Звоним на", Value: addr},
		{Label: "Вносит запросы в", Value: s.eng.GatewayUpstream()},
		{Label: "Потоков сейчас", Value: fmt.Sprintf("%d из %d", stats.Streams, s.eng.GatewayMaxStreams())},
		{Label: "Потоков за запуск", Value: fmt.Sprintf("%d", stats.Total)},
	}
	if stats.Denials > 0 {
		props = append(props, Prop{Label: "Потоки не открылись", Value: fmt.Sprintf("%d · последняя причина: %s",
			stats.Denials, stats.ErrText), Color: "#C4271C"})
	}
	caption := "соединение открывает Сёма: дома не нужен ни один открытый порт"
	if !live {
		caption = "Сёма звонит и перезванивает сам — снаружи вход не работает, дома всё как прежде"
	}
	if live {
		if look, err := s.eng.GatewayAsk(context.Background()); err == nil {
			props = append(props,
				Prop{Label: "Домен (у шлюза)", Value: emptyDash(look.Domain)},
				Prop{Label: "Край держит", Value: emptyDash(look.Edge)},
				Prop{Label: "Публичный сертификат", Value: emptyDash(look.Cert)},
				Prop{Label: "Браузеров принимает на", Value: emptyDash(look.Browsers)},
				Prop{Label: "Пропущено запросов", Value: fmt.Sprintf("%d", look.Skipped)},
				Prop{Label: "Отсеяно на краю", Value: fmt.Sprintf("%d", look.Excluded)},
				Prop{Label: "Адресов закрыто", Value: fmt.Sprintf("%d", look.Closed)},
				// Своя графа, а не слагаемое отсева: отсев — решение края, а
				// «не внесено» — беда дома, и чинить их в разных местах
				Prop{Label: "Пропустил, а внести не смог", Value: fmt.Sprintf("%d", look.NotRecorded)})
			if look.Level == LevelWarn || look.Level == LevelAlert {
				level = look.Level
				pill = Badge{Text: "сертификат: посмотрите", Level: look.Level}
			}
		} else {
			props = append(props, Prop{Label: "Он о себе", Value: "не рассказал: " + err.Error() +
				" — это отказ шлюза, а не Сёмы", Color: "#C4271C"})
		}
	}
	row := Component{Type: "row", Layout: "wide", ID: "gateway", Subtitle: "Шлюз «" + agent.Name + "»",
		Caption: caption, Badges: []Badge{pill}, Props: props, Expandable: true, Expanded: true,
		Actions: []Action{{Action: "$goto", Target: entityAddr(agentsScreen, agent.Name, ""), Label: agent.Name}}}
	withLevel(&row, level)
	return row
}

// emptyDash — пустое значение не должно выглядеть как «всё хорошо».
func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "— не сказал"
	}
	return s
}

// allowedForRole — какие способности роли положены в принципе. Человек должен
// видеть не только то, что агент объявил, но и то, чего ему по роли не
// достанется никогда, — иначе пустой список выглядит поломкой.
func allowedForRole(role string) string {
	switch role {
	case engine.RoleResident:
		return "код, память, уведомления"
	case engine.RoleGateway:
		return "шлюз"
	}
	return "роль незнакомая — способностей не выдаётся ни одной"
}

// hasCan — разрешена ли агенту способность. Строка про память нужна там,
// где память вообще включена: у остальных это лишний шум.
func hasCan(a *store.Agent, can string) bool {
	for _, c := range a.Allowed {
		if c == can {
			return true
		}
	}
	return false
}

// ---- «Журнал» ----

// agentsLogFilters — чипы журнала по порядку: слово отбора и подпись.
var agentsLogFilters = []struct{ ID, Title string }{
	{store.AgentLogAll, "Все"},
	{store.AgentLogCalls, "Вызовы"},
	{store.AgentLogFail, "Неудачи"},
}

// agentJournal — журнал агента (пустое имя — всех): чипы отбора с числами, первая
// страница и «Показать ещё». Добавляется к документу как есть. Хроника, а не
// настройка: на первом экране агента её нет, она приезжает с вкладкой.
func (s *Server) agentJournal(doc *Document, agent, filter string, limit int) error {
	st := s.eng.Store()
	doc.noPhoneSearch = true
	known := false
	for _, f := range agentsLogFilters {
		doc.Filters = append(doc.Filters, Filter{ID: f.ID,
			Title: fmt.Sprintf("%s · %d", f.Title, st.CountAgentLog(agent, f.ID))})
		if f.ID == filter {
			known = true
		}
	}
	if !known {
		filter = store.AgentLogAll
	}
	doc.ActiveFilter = filter

	if limit <= 0 || limit == defaultRows {
		limit = agentsLogPage
	}
	entries, err := st.AgentLogFiltered(agent, filter, limit+1)
	if err != nil {
		return err
	}
	more := len(entries) > limit
	if more {
		entries = entries[:limit]
	}
	rows := make([]Component, 0, len(entries))
	for i, u := range entries {
		rows = append(rows, agentLogRow(u, agent == "", i))
	}
	if len(rows) == 0 {
		rows = append(rows, emptyState("Записей нет",
			"Сюда попадает всё, что идёт через агента: вход, права, отзыв и каждый вызов способности — кто спрашивал, чем кончилось", "list.bullet"))
		if filter != store.AgentLogAll {
			rows[0] = emptyState("Под этот отбор ничего не подошло", "Выберите «Все» — там вся хроника этого журнала", "list.bullet")
		}
	}
	title := "О чём спрашивали"
	if agent != "" {
		title = "Что шло через «" + agent + "»"
	}
	doc.Components = append(doc.Components, Component{Type: "section", Title: title, Children: collapseSame(rows, 3)})
	if more {
		total := st.CountAgentLog(agent, filter)
		doc.Components = append(doc.Components, Component{Type: "button", Actions: []Action{
			{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d из %d", len(rows), total)}}})
	}
	return nil
}

// agentsLogFragment — тело журнала всех агентов: двадцать строк и «Показать
// ещё». Хроника приезжает по раскрытию, а не вместе с экраном.
func (s *Server) agentsLogFragment(_ string, _ string, limit int) ([]Component, error) {
	st := s.eng.Store()
	entries, err := st.AgentLogFiltered("", store.AgentLogAll, limit+1)
	if err != nil {
		return nil, err
	}
	more := len(entries) > limit
	if more {
		entries = entries[:limit]
	}
	rows := make([]Component, 0, len(entries)+1)
	for i, u := range entries {
		rows = append(rows, agentLogRow(u, true, i))
	}
	if len(rows) == 0 {
		return []Component{emptyState("Записей нет",
			"Сюда попадает всё, что идёт через агентов: вход, права, отзыв и каждый вызов способности", "list.bullet")}, nil
	}
	if more {
		rows = append(rows, moreInFragment(len(entries), st.CountAgentLog("", store.AgentLogAll)))
	}
	return rows, nil
}

// agentLogRow — строка журнала. Имя агента в заголовке — только у общего
// журнала: у журнала самого агента оно лишнее.
func agentLogRow(u store.AgentUse, withAgent bool, n int) Component {
	level := LevelOK
	if !u.OK {
		level = LevelAlert
	}
	title := u.What
	if withAgent {
		title = u.Agent + " · " + u.What
	}
	return Component{Type: "row", Layout: "wide", ID: "agent-log-" + strconv.Itoa(n), Level: level, Dot: levelDot(level),
		Subtitle: title, Caption: whenText(u.At) + " · " + u.Detail}
}

// sortedNames — имена агентов для переключателя в шапке.
func sortedNames(agents []*store.Agent) []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.Name)
	}
	sort.Strings(out)
	return out
}
