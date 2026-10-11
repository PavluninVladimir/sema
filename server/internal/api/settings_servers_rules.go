package api

// Вкладка «Правила» экрана сервера: у каждого сервера свои правила доступа —
// три колонки образцов («Разрешить», «Спросить», «Запретить») с добавлением
// и удалением, поле «Проверить команду» и наборы «Linux» и «RouterOS» для
// сервера без правил. Макет — design/pravila/Pravila.html; до 1 октября 2026
// правила жили вкладкой раздела «Серверы» с выбором сервера карточками, а
// теперь сервер уже выбран: правила — часть его экрана.
//
// «Проверить команду» отвечает тем же `Engine.Decide`, которым решается
// настоящий запрос: второй копии логики в браузере нет. Проверяемая команда
// едет в адресе экрана (`~try:`) в виде base64, чтобы `&`, `|` и пробелы не
// ломали ни ссылку, ни хэш страницы.

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// rulesTry — хвост адреса с проверяемой командой.
const rulesTry = "~try:"

// rulesAddr — адрес вкладки правил сервера; с командой — и с проверкой.
func rulesAddr(host, cmd string) string {
	addr := entityAddr(serversScreen, host, serversTabRules)
	if strings.TrimSpace(cmd) != "" {
		addr += rulesTry + base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(cmd)))
	}
	return addr
}

// decodeTry — проверяемая команда из хвоста адреса (base64).
func decodeTry(try string) string {
	if try == "" {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(try)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// policyLevel — цвет ответа: разрешить — зелёный, спросить — жёлтый,
// запретить — красный.
func policyLevel(p string) string {
	switch p {
	case engine.PolicyAllow:
		return LevelOK
	case engine.PolicyAsk:
		return LevelWarn
	}
	return LevelAlert
}

// hostsWithoutRules — сколько серверов без единого правила: на них любая
// команда запрещена, и строка списка говорит об этом.
func hostsWithoutRules(st *store.Store, hosts []*store.Host) int {
	n := 0
	for _, h := range hosts {
		if st.HostRuleSet(h.Name).Empty() {
			n++
		}
	}
	return n
}

// serverRules — содержимое вкладки «Правила» одного сервера.
func (s *Server) serverRules(add func(Component), cur *store.Host, try string) {
	st := s.eng.Store()
	tryCmd := decodeTry(try)

	list := st.HostRuleSet(cur.Name)
	head := Component{Type: "row", Layout: "wide", ID: "rules-head-" + cur.Name,
		Subtitle: "Команды · " + cur.Name,
		Caption:  "у каждого сервера свои правила; команду нельзя перечислить заранее, поэтому здесь образцы",
		Badges: []Badge{
			{Text: "Разрешить · " + strconv.Itoa(len(list.Allow)), Level: LevelOK},
			{Text: "Спросить · " + strconv.Itoa(len(list.Ask)), Level: LevelWarn},
			{Text: "Запретить · " + strconv.Itoa(len(list.Deny)), Level: LevelAlert},
		}}
	if !list.Empty() {
		// набор дописывает образцы, а не заменяет: убранное и добавленное
		// самим остаётся как есть
		for _, set := range store.RuleSets {
			head.Menu = append(head.Menu, Action{Action: "take_rule_set", Target: cur.Name + "~" + set,
				Label:   "Добавить набор " + store.RuleSetTitle(set),
				Confirm: "Дописать серверу «" + cur.Name + "» образцы набора «" + store.RuleSetTitle(set) + "»? То, что уже есть, не повторится и не потеряется.", ConfirmOK: "Дописать"})
		}
	}
	add(head)

	if list.Empty() {
		empty := Component{Type: "row", Layout: "wide", ID: "rules-empty-" + cur.Name, Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "У сервера нет правил — любая команда запрещена",
			Caption:  "Возьмите готовый набор или впишите образцы сами. Пока правил нет, Сёма не выполнит на «" + cur.Name + "» ни одной команды — даже «uptime».",
		}
		for _, set := range store.RuleSets {
			a := Action{Action: "take_rule_set", Target: cur.Name + "~" + set, Label: "Набор " + store.RuleSetTitle(set)}
			if set == store.RuleSetLinux && cur.Kind != store.HostRouterOS || set == store.RuleSetRouterOS && cur.Kind == store.HostRouterOS {
				a.Style = "primary"
			}
			empty.Actions = append(empty.Actions, a)
		}
		add(empty)
	}

	// проверка команды: тем же решением, что и настоящий запрос
	add(Component{Type: "form", ID: "rules-try-" + cur.Name, Title: "Проверить команду",
		Fields: []Field{{Type: "text", Name: "cmd", Label: "Что Сёма сделает с командой",
			Hint: "ничего не выполняется: показывается только ответ правил сервера «" + cur.Name + "»", Value: tryCmd}},
		Submit: &Action{Action: "check_host_rule", Target: cur.Name, Label: "Проверить"}})
	var hits map[string]bool
	if strings.TrimSpace(tryCmd) != "" {
		r := s.eng.Decide(cur.Name, tryCmd)
		hits = map[string]bool{}
		for _, p := range r.Rules {
			hits[strings.ToLower(strings.TrimSpace(p))] = true
		}
		verdict := Component{Type: "row", Layout: "wide", ID: "rules-verdict", Level: policyLevel(r.Policy), Dot: levelDot(policyLevel(r.Policy)),
			Subtitle: tryCmd, Caption: r.Why,
			Badges: []Badge{{Text: engine.PolicyTitle(r.Policy), Level: policyLevel(r.Policy)}}}
		if r.Default && !r.NoSudo {
			// правила нет — его можно завести прямо отсюда; что разрешить
			// нельзя (sudo, неясный синтаксис), предлагается только «Спросить»
			verdict.Actions = append(verdict.Actions, Action{Action: "add_host_rule", Target: cur.Name + "~" + store.RuleAsk, Label: "Спрашивать для неё", Style: "primary",
				Prompt: "Образец для правила «Спросить»: команда целиком или её начало", Value: tryCmd})
			if r.CanAllow() {
				verdict.Actions = append(verdict.Actions, Action{Action: "add_host_rule", Target: cur.Name + "~" + store.RuleAllow, Label: "Разрешить её",
					Prompt: "Образец для правила «Разрешить»: команда целиком или её начало", Value: tryCmd})
			}
		}
		add(verdict)
	}

	// три колонки образцов
	column := func(kind, title, explain string) Component {
		rules := s.eng.Store().HostRules(cur.Name)
		var chips []Component
		// новые — первыми: только что добавленное должно быть на виду, а не
		// в хвосте прокручиваемой колонки
		for i := len(rules) - 1; i >= 0; i-- {
			r := rules[i]
			if r.Kind != kind {
				continue
			}
			chip := Component{Text: r.Pattern, Actions: []Action{{Action: "delete_host_rule", Target: strconv.FormatInt(r.ID, 10), Label: "×"}}}
			if hits[strings.ToLower(strings.TrimSpace(r.Pattern))] {
				chip.Style = "hit"
			}
			chips = append(chips, chip)
		}
		return Component{ID: "rules-" + kind, Title: title, Caption: explain, Level: policyLevel(kind), Children: chips,
			Actions: []Action{{Action: "add_host_rule", Target: cur.Name + "~" + kind, Label: "Добавить"}}}
	}
	add(Component{Type: "patterns", ID: cur.Name, Children: []Component{
		column(store.RuleAllow, "Разрешить", "команда начинается с образца — выполняется сразу"),
		column(store.RuleAsk, "Спросить", "образец встречается в команде — ждёт вашей кнопки (у самостоятельного сервера — суда); sudo — только здесь"),
		column(store.RuleDeny, "Запретить", "слово-образец в команде — отказ без вопроса, даже с вашей кнопкой"),
	}})
	add(Component{Type: "section", ID: "rules-how", Title: "Как это устроено", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary",
		Text: "Не подошло ни одно правило — запрещено. sudo, doas и su выполняются только по правилу «Спросить» и всегда с вашей кнопкой (или с судом двух моделей, если сервер отдан Сёме). " +
			"Запрет сильнее всего остального. «Разрешить» не действует на команду с sudo, перенаправлением в файл и неясным синтаксисом, а чистую читающую команду " +
			"«Спросить» не останавливает: слово «sshd_config» из «Спросить» не мешает «cat /etc/ssh/sshd_config» — оно берегло изменения, а не взгляд. " +
			"Поэтому пару «Разрешить docker» и «Спросить docker rm» Сёма не принимает: сначала сузьте одно из двух. " +
			"У «Полной» самостоятельности «Спросить» идёт на суд двух моделей со страховкой возврата, «Запретить» не меняется никогда. " +
			"Пределы времени и вывода SSH — на вкладке «Самостоятельность» раздела, в «Тонкой настройке»."}}})
}

// serverKindWord — род сервера словами для шапки.
func serverKindWord(h *store.Host) string {
	switch h.Kind {
	case store.HostLinux:
		return "Linux"
	case store.HostRouterOS:
		return "RouterOS"
	}
	return "род не определён"
}

// ---- действия ----

// sshRuleActions — правки правил сервера: добавить, убрать, взять набор,
// проверить команду, решить по команде без правила.
func (s *Server) sshRuleActions(req actionRequest) (string, error) {
	st := s.eng.Store()
	switch req.Action {
	case "add_host_rule":
		host, kind, ok := strings.Cut(req.Target, "~")
		if !ok {
			return "", fmt.Errorf("нужен сервер и ответ правила, например WireGuard~ask")
		}
		// пара «Разрешить docker» и «Спросить docker rm» не работала бы: вместо
		// молчаливого правила человек получает объяснение, что сузить
		if err := s.eng.CheckRuleAdd(host, kind, req.Params["value"]); err != nil {
			return "", err
		}
		if err := st.AddHostRule(host, kind, req.Params["value"]); err != nil {
			return "", err
		}
		return rulesAddr(host, ""), nil
	case "delete_host_rule":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", fmt.Errorf("нужен номер правила")
		}
		host := ""
		if r, ok := st.HostRuleByID(id); ok {
			host = r.Host
		}
		if err := st.DeleteHostRule(id); err != nil {
			return "", err
		}
		return rulesAddr(host, ""), nil
	case "take_rule_set":
		host, set, ok := strings.Cut(req.Target, "~")
		if !ok {
			return "", fmt.Errorf("нужен сервер и набор, например WireGuard~linux")
		}
		if _, err := s.eng.TakeRuleSet(host, set); err != nil {
			return "", err
		}
		return rulesAddr(host, ""), nil
	case "check_host_rule":
		if _, ok := st.Host(req.Target); !ok {
			return "", fmt.Errorf("сервера %q нет", req.Target)
		}
		return rulesAddr(req.Target, req.Params["cmd"]), nil
	case "rule_ask", "rule_allow", "rule_dismiss":
		// решение по команде, у которой не нашлось правила; после него она идёт
		// своим путём и может идти долго (суд, выполнение), поэтому не держим запрос
		id, err := req.one()
		if err != nil {
			return "", fmt.Errorf("нужен номер запроса на правило")
		}
		answer := map[string]string{"rule_ask": store.NoRuleAsk, "rule_allow": store.NoRuleAllow, "rule_dismiss": store.NoRuleDismissed}[req.Action]
		who := req.who
		if who == "" {
			who = "человек"
		}
		// правило и ошибки решения — сразу, а команда идёт дальше в фоне: суд и
		// выполнение на сервере занимают время, которого нажатие ждать не должно
		if err := s.eng.DecideNoRuleAsync(context.Background(), id, answer, who); err != nil {
			return "", err
		}
		if strings.HasPrefix(req.Screen, serversScreen) {
			return req.Screen, nil
		}
		return "", nil
	}
	return "", errUnhandled
}
