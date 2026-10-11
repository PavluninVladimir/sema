package engine

// Правила доступа: три ответа — «Разрешить» (allow), «Спросить» (ask),
// «Запретить» (deny), как в Claude Code. У каждого сервера свои правила.
// Не подошло ни одно правило — запрещено: у нового сервера правил нет, и
// любая команда на нём отказана, пока человек не возьмёт набор или не
// впишет образцы сам.
//
// Порядок решения:
//
//  1. запрет — образец «Запретить» по началу слова в любой части команды:
//     сильнее всего остального;
//  2. разрешено — каждая часть команды начинается с образца «Разрешить», и
//     при этом команда чистая: без sudo, перенаправления в файл,
//     изменяющего ключа и неясного синтаксиса (shellSafety). Чистота —
//     условие разрешения, а не отдельный ответ: образец «df» открывает
//     «df -h» и не открывает «df > /etc/passwd»;
//  3. спросить — образец «Спросить» встречается в команде. Любой sudo,
//     doas и su, перенаправление и неясный синтаксис проходят только здесь;
//  4. иначе запрещено.
//
// «Разрешить» стоит перед «Спросить» для команды, прошедшей чистую проверку, и
// это не то, что у Claude Code (там «Спросить» всегда сильнее), а то, что
// прежний порядок кругов выучил на живых данных: образец «Спросить» держит
// ИЗМЕНЕНИЯ ключей, пользователей и файрвола, а не взгляд на них. «cat
// /etc/ssh/sshd_config», «getent passwd», «grep -c '' authorized_keys» и
// «ufw status» остаются чтением, хотя «sshd_config», «passwd»,
// «authorized_keys» и «ufw» стоят у сервера в «Спросить». Будь «Спросить»
// сильнее, ночной осмотр ключей и пользователей выпал бы целиком — замер по
// скиллам: 2 из 20 команд осмотра Linux и 6 из 20 у RouterOS (`/user print`,
// `/ip firewall … print`). Всё, за что разрешение не вправе ручаться —
// изменяющее, с sudo, с неясным синтаксисом, — «Спросить» перехватывает по
// вхождению, как и прежде.
//
// Расплата — пара противоречивых правил: «Разрешить docker» и «Спросить
// docker rm» вместе значили бы «docker rm идёт без вопроса», и человек
// ожидал бы обратного. Поэтому экран такую пару не принимает (`CheckRuleAdd`):
// он говорит, какое из двух правил сузить, а не молчит.
//
// Одно исключение из чистоты: команда, вписанная в «Разрешить» ЦЕЛИКОМ,
// слово в слово (`whole`). Это решение человека про одну конкретную
// строку, а не послабление образца: осмотр «find … -exec md5sum {} +»
// читает, хотя по ключам похож на изменяющий. Дописанный хвост делает
// строку другой, и разрешение к ней не относится; sudo целиком не
// разрешается никогда.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Ответы правила — разряд команды (`Access Policy`).
const (
	PolicyAllow = store.RuleAllow // выполняется сразу, видно в журнале
	PolicyAsk   = store.RuleAsk   // ждёт кнопки человека (у самостоятельного сервера — суда)
	PolicyDeny  = store.RuleDeny  // не выполняется
)

// PolicyTitle — ответ словами человека.
func PolicyTitle(p string) string {
	switch p {
	case PolicyAllow:
		return "Разрешить"
	case PolicyAsk:
		return "Спросить"
	case PolicyDeny:
		return "Запретить"
	}
	return p
}

// PolicyFromWord — ответ по слову человека или по английскому имени.
func PolicyFromWord(w string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(w)) {
	case "разрешить", "allow":
		return PolicyAllow, true
	case "спросить", "ask":
		return PolicyAsk, true
	case "запретить", "deny":
		return PolicyDeny, true
	}
	return "", false
}

// Ruling — решение по команде на сервере: ответ, причина словами человека и
// то, по чему решено.
type Ruling struct {
	Policy string
	Why    string
	// Rules — образцы, по которым решено: экран подсвечивает их в колонках
	Rules []string
	// Default — ни одно правило не подошло: запрещено по умолчанию. Тут,
	// и только тут, человек может завести правило кнопкой; запрет по
	// правилу «Запретить» кнопок не имеет
	Default bool
	// NoRules — у сервера правил нет вовсе
	NoRules bool
	// NoSudo — у сервера выключен sudo, а команда просит root: запрещено
	// настройкой сервера, сильнее любых правил
	NoSudo bool
	// Unsafe — чем команда не годится для правила «Разрешить»: sudo,
	// перенаправление, изменяющий ключ, неясный синтаксис. Пусто — годится.
	// Такая команда бывает только «Спросить» или запрещена
	Unsafe string
}

// CanAllow — можно ли предложить человеку «Разрешить её».
func (r Ruling) CanAllow() bool { return r.Unsafe == "" && !r.NoSudo }

// Decide — решение по команде на сервере по правилам самого сервера.
func (e *Engine) Decide(host, cmd string) Ruling {
	r := decideSSH(e.store.HostRuleSet(host), cmd)
	// «без sudo» у сервера сильнее любых правил: человек сказал, что root
	// на этой машине Сёме не даётся, — значит такую команду не показываем
	// даже кнопкой
	if h, ok := e.store.Host(host); ok && h.NoSudo && hasSudo(cmd) {
		return Ruling{Policy: PolicyDeny, NoSudo: true, Unsafe: "sudo",
			Why: "у сервера «" + host + "» выключен sudo: root Сёме здесь не даётся. " +
				"Нужное делается узкими правами в /etc/sudoers.d на самом сервере плюс «Разрешить sudo» в строке сервера"}
	}
	return r
}

// Classify — ответ и причина: то же решение без подробностей.
func (e *Engine) Classify(host, cmd string) (policy, why string) {
	r := e.Decide(host, cmd)
	return r.Policy, r.Why
}

// decideSSH — решение по правилам, без базы: чистая функция от списка
// образцов и команды. Вынесена, чтобы свойства проверялись и без сервера.
func decideSSH(rules store.RuleList, cmd string) Ruling {
	low := strings.ToLower(strings.TrimSpace(cmd))
	out := Ruling{Unsafe: sshUnsafe(cmd, low)}

	// 1. запрет
	for _, p := range rules.Deny {
		if p = strings.TrimSpace(p); p != "" && denyHit(low, strings.ToLower(p)) {
			out.Policy, out.Why, out.Rules = PolicyDeny, "запрещено правилом «Запретить»: "+p, []string{p}
			return out
		}
	}
	if rules.Empty() {
		out.Policy, out.Default, out.NoRules = PolicyDeny, true, true
		out.Why = "у сервера нет правил — любая команда запрещена"
		return out
	}

	// 2. разрешено: целиком, вписанной человеком строкой
	if !withSudo(low) {
		if p, ok := wholeMatch(low, rules.Allow); ok {
			out.Policy, out.Why, out.Rules = PolicyAllow, "человек разрешил эту команду целиком", []string{p}
			return out
		}
	}
	// ... или каждая часть началом образца, и команда чистая
	if out.Unsafe == "" {
		if used, ok := coveredBy(low, rules.Allow); ok {
			out.Policy, out.Rules = PolicyAllow, used
			out.Why = "разрешено правилом: " + strings.Join(used, ", ")
			return out
		}
	}

	// 3. спросить
	for _, p := range rules.Ask {
		if p = strings.TrimSpace(p); p != "" && askHit(low, strings.ToLower(p)) {
			out.Policy, out.Rules = PolicyAsk, []string{p}
			out.Why = "правило «Спросить»: " + p
			return out
		}
	}

	// 4. не подошло ни одно правило
	out.Policy, out.Default = PolicyDeny, true
	switch {
	case out.Unsafe != "":
		out.Why = out.Unsafe + " — такое проходит только по правилу «Спросить», а подходящего у сервера нет"
	default:
		out.Why = "ни одно правило сервера не подошло — запрещено"
		if w := firstUncovered(low, rules.Allow); w != "" {
			out.Why += ": нет правила для «" + w + "»"
		}
	}
	return out
}

// sshUnsafe — чем команда не годится для правила «Разрешить». Пусто —
// чистая: можно судить по образцам. Проверки те же, что стояли перед
// кругом чтения: неясный синтаксис, sudo, перенаправление в файл,
// изменяющий ключ у читающей по имени утилиты.
func sshUnsafe(cmd, low string) string {
	// подстановка $(…) и `…` — команда внутри команды: судим и её, иначе
	// «echo $(touch …)» читалось бы как echo
	if why := shellSafety(cmd); why != "" {
		return why
	}
	parts := splitCommand(low)
	for _, inner := range innerCommands(low) {
		parts = append(parts, splitCommand(inner)...)
	}
	for _, part := range parts {
		// скобки группировки (a && b) — не команда: каждую часть судим отдельно
		part = strings.Trim(strings.TrimSpace(part), "() \t")
		if part == "" {
			continue
		}
		switch firstWord(part) {
		case "sudo", "doas", "su":
			return "sudo, doas и su"
		}
		// перенаправление в файл меняет сервер, даже если команда читающая;
		// /dev/null — единственное исключение
		if redirectsToFile(part) {
			return "перенаправление вывода в файл"
		}
		if why := mutatingKey(part); why != "" {
			return why
		}
	}
	return ""
}

// partsOf — части команды для сверки с образцами: по | ; && ||, вместе с
// командами внутри подстановок, без скобок группировки и пустых.
func partsOf(low string) []string {
	parts := splitCommand(low)
	for _, inner := range innerCommands(low) {
		parts = append(parts, splitCommand(inner)...)
	}
	var out []string
	for _, part := range parts {
		if part = strings.Trim(strings.TrimSpace(part), "() \t"); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// allowMatch — часть команды подходит образцу «Разрешить»: образец с «*» —
// маска на всю часть («/* print*» покрывает все print RouterOS), без «*» —
// начало команды по границе слова.
func allowMatch(part, pattern string) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if p == "" {
		return false
	}
	if strings.Contains(p, "*") {
		return globMatch(p, part)
	}
	return part == p || strings.HasPrefix(part, p+" ")
}

// coveredBy — каждая часть команды подходит какому-то образцу «Разрешить».
// Возвращает образцы, по которым прошли части.
func coveredBy(low string, allow []string) ([]string, bool) {
	parts := partsOf(low)
	if len(parts) == 0 {
		return nil, false
	}
	var used []string
	seen := map[string]bool{}
	for _, part := range parts {
		hit := ""
		for _, p := range allow {
			if allowMatch(part, p) {
				hit = strings.TrimSpace(p)
				break
			}
		}
		if hit == "" {
			return nil, false
		}
		if !seen[hit] {
			seen[hit] = true
			used = append(used, hit)
		}
	}
	return used, true
}

// firstUncovered — первая часть команды, для которой нет образца
// «Разрешить»: по ней человеку говорят, чего не хватает.
func firstUncovered(low string, allow []string) string {
	for _, part := range partsOf(low) {
		ok := false
		for _, p := range allow {
			if allowMatch(part, p) {
				ok = true
				break
			}
		}
		if !ok {
			return firstWord(part)
		}
	}
	return ""
}

// wholeMatch — вся команда совпала с образцом «Разрешить» слово в слово.
// Хвостовые пробелы и регистр не в счёт, всё остальное — да: разрешение
// дано ровно этой строке.
func wholeMatch(low string, allow []string) (string, bool) {
	for _, p := range allow {
		if q := strings.ToLower(strings.TrimSpace(p)); q != "" && q == low {
			return strings.TrimSpace(p), true
		}
	}
	return "", false
}

// denyHit — образец «Запретить» встречается в команде по началу слова. С
// «*» — маска, начинающаяся на границе слова.
func denyHit(low, p string) bool {
	if !strings.Contains(p, "*") {
		return containsWord(low, p)
	}
	re := regexp.MustCompile(`(?:^|[ \t;|&(])` + strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, ".*"))
	return re.MatchString(low)
}

// askHit — образец «Спросить» встречается в команде где угодно: «sshd_config»
// ловит и «nano /etc/ssh/sshd_config». С «*» — маска внутри команды.
func askHit(low, p string) bool {
	if !strings.Contains(p, "*") {
		return strings.Contains(low, p)
	}
	return globMatch("*"+p+"*", low)
}

// CheckRuleAdd — можно ли завести правило так, чтобы оно действовало. Пара
// «Разрешить» и «Спросить», где разрешающий образец начинает команду, а
// спрашивающий её продолжает («docker» и «docker rm»), не работает: чистая
// команда разрешена, и «Спросить» её не остановило бы. Вместо молчаливого
// правила, которое ничего не делает, человек получает объяснение, что
// сузить. Запрет ни с чем не спорит — он сильнее всего — и проверки не
// требует; образец с «*» и слово из аргументов («sshd_config») — тоже.
func (e *Engine) CheckRuleAdd(host, kind, pattern string) error {
	pattern = strings.TrimSpace(pattern)
	low := strings.ToLower(pattern)
	if pattern == "" || strings.Contains(low, "*") {
		return nil
	}
	rules := e.store.HostRuleSet(host)
	switch kind {
	case PolicyAsk:
		// «Спросить» на то, что уже разрешено как чистая команда, не подействует
		if r := decideSSH(store.RuleList{Allow: rules.Allow}, pattern); r.Policy == PolicyAllow && len(r.Rules) > 0 {
			return fmt.Errorf("«Спросить» на «%s» не подействует: эта команда уже разрешена образцом «%s», а «Разрешить» стоит перед «Спросить». "+
				"Сузьте «Разрешить» (вместо «%s» впишите то, что можно) или поставьте «Запретить»", pattern, r.Rules[0], r.Rules[0])
		}
	case PolicyAllow:
		// «Разрешить» открыло бы команду, которую сервер спрашивает по образцу-началу
		for _, k := range rules.Ask {
			k = strings.ToLower(strings.TrimSpace(k))
			if k == "" || strings.Contains(k, "*") || (k != low && !strings.HasPrefix(k, low+" ")) {
				continue
			}
			return fmt.Errorf("«Разрешить» на «%s» откроет и «%s», который у сервера спрашивается: «Разрешить» стоит перед «Спросить». "+
				"Впишите образец уже или уберите «Спросить»", pattern, k)
		}
	}
	return nil
}
