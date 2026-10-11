package engine

// SSH: серверы из справочника, ключ из хранилища по имени, у каждого
// сервера свои правила доступа (разрешить — сразу, спросить — кнопка
// человека, запретить — никогда; не подошло ни одно правило — запрещено),
// журнал каждой команды и каждого обращения к ключу. Решает по правилам
// `Decide` (ssh_rules.go).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/sshx"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	sshTimeoutKey   = "ssh.timeout_s"
	sshMaxOutputKey = "ssh.max_output"
)

func (e *Engine) SSHTimeout() int { return e.store.SettingInt(sshTimeoutDef) }

// SSHConfirmTimeout — предел для команды, подтверждённой человеком:
// apt update и upgrade не укладываются в общий таймаут чтения.
func (e *Engine) SSHConfirmTimeout() int { return e.store.SettingInt(sshConfirmTimeoutDef) }

// sshTimeoutCtx — переопределение таймаута через контекст (подтверждённые команды).
type sshTimeoutCtx struct{}

func (e *Engine) SSHMaxOutput() int { return e.store.SettingInt(sshMaxOutputDef) }

func (e *Engine) SetSSHSetting(key string, v int) error {
	return e.store.SetSetting(key, fmt.Sprint(v))
}

// withSudo — есть ли в команде sudo/doas/su хоть одной частью. Такое не
// разрешается никакой кнопкой: у «Выполнить и впредь без вопроса» для
// sudo кнопки нет, и здесь тоже.
func withSudo(low string) bool {
	for _, part := range splitCommand(low) {
		part = strings.Trim(strings.TrimSpace(part), "() \t")
		for _, s := range []string{"sudo", "doas", "su"} {
			if part == s || strings.HasPrefix(part, s+" ") {
				return true
			}
		}
	}
	return false
}

// headTail — начало и конец длинного вывода: ошибка apt или dpkg живёт в
// последних строках, а не в первых.
func headTail(s string, head, tail int) string {
	r := []rune(s)
	if len(r) <= head+tail+20 {
		return s
	}
	return string(r[:head]) + "\n… (пропущено " + strconv.Itoa(len(r)-head-tail) + " символов) …\n" + string(r[len(r)-tail:])
}

// globMatch — образец с «*» (любые символы) против всей части команды:
// «/* print*» покрывает все print RouterOS.
func globMatch(pattern, part string) bool {
	re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
	ok, _ := regexp.MatchString(re, part)
	return ok
}

var reRedirect = regexp.MustCompile(`(^|[^0-9&<>])[0-9]?>>?\s*([^\s&]+)`)

func redirectsToFile(part string) bool {
	for _, m := range reRedirect.FindAllStringSubmatch(part, -1) {
		target := m[2]
		if strings.HasPrefix(target, "=") { // сравнение >= внутри awk, не перенаправление
			continue
		}
		if target != "/dev/null" {
			return true
		}
	}
	return false
}

// hasSudo — команда просит root: sudo, doas или su в любой части, в том
// числе внутри подстановки.
func hasSudo(cmd string) bool {
	low := strings.ToLower(cmd)
	parts := splitCommand(low)
	for _, inner := range innerCommands(low) {
		parts = append(parts, splitCommand(inner)...)
	}
	for _, p := range parts {
		p = strings.Trim(strings.TrimSpace(p), "() \t")
		w := firstWord(p)
		if w == "sudo" || w == "doas" || w == "su" || strings.HasPrefix(p, "sudo ") || strings.HasPrefix(p, "doas ") {
			return true
		}
	}
	return false
}

// containsWord — образец «никогда» встречается как отдельное слово или
// начало слова, а не внутри имени файла: «reboot» ловит «sudo reboot» и
// «/system reboot», но не «/var/run/reboot-required».
// словоЦеликом — образец написан одним словом («dd», «reboot», «mkfs»),
// а не куском строки с пробелами и косыми («> /dev/sd», «rm -rf /»).
// Куски нарочно написаны началом и обязаны ловить продолжение: «/dev/sd»
// должен поймать «/dev/sda».
func wordWhole(pattern string) bool {
	for i := 0; i < len(pattern); i++ {
		if !wordContinues(pattern[i]) && pattern[i] != '.' {
			return false
		}
	}
	return pattern != ""
}

// wordContinues — знак, которым слово может продолжаться. Если после
// образца стоит такой, значит это другое слово: «ddrescue» не «dd»,
// «evaluate» не «eval», «reboot-required» не «reboot».
func wordContinues(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return b == '-' || b == '_'
}

func containsWord(cmd, pattern string) bool {
	for i := 0; ; {
		j := strings.Index(cmd[i:], pattern)
		if j < 0 {
			return false
		}
		pos := i + j
		end := pos + len(pattern)
		// Слева — начало строки или разделитель. Справа особый случай:
		// дефис и подчёркивание означают, что это другое имя, а не наша
		// команда. «echo REBOOT_REQUIRED» и «/var/run/reboot-required»
		// считались перезагрузкой, и утренняя проверка серверов не могла
		// узнать, нужен ли перезапуск, — ровно то, ради чего её заводили.
		// Буквы и точку справа оставляем: образцы вроде «mkfs» и
		// «> /dev/sd» нарочно написаны началом слова и обязаны ловить
		// «mkfs.ext4» и «/dev/sda»
		left := pos == 0 || strings.ContainsRune(" \t;|&(", rune(cmd[pos-1]))
		right := end >= len(cmd) || !wordWhole(pattern) || !wordContinues(cmd[end])
		if left && right {
			return true
		}
		i = pos + 1
		if i >= len(cmd) {
			return false
		}
	}
}

// splitCommand делит команду по | ; && || — но не внутри кавычек: у awk и
// grep -E вертикальная черта в аргументе — не конвейер.
func splitCommand(cmd string) []string {
	var parts []string
	var cur strings.Builder
	quote := rune(0)
	flush := func() { parts = append(parts, cur.String()); cur.Reset() }
	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case r == '|' || r == ';' || r == '\n':
			flush()
			if r == '|' && i+1 < len(rs) && rs[i+1] == '|' {
				i++
			}
		case r == '&' && i+1 < len(rs) && rs[i+1] == '&':
			flush()
			i++
		case r == '&' && !(i > 0 && rs[i-1] == '>'):
			// одиночный & — вторая команда в фоне; «2>&1» — перенаправление
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return parts
}

// innerCommands — команды внутри подстановок $(…) и `…`. Внутри одинарных
// кавычек оболочка ничего не подставляет, там не ищем.
func innerCommands(cmd string) []string {
	var out []string
	rs := []rune(cmd)
	quote := rune(0) // открытая кавычка: внутри двойных апостроф — просто буква
	for i := 0; i < len(rs); i++ {
		switch {
		case quote == '\'':
			if rs[i] == '\'' {
				quote = 0
			}
		case rs[i] == '\'' && quote == 0:
			quote = '\''
		case rs[i] == '"':
			if quote == '"' {
				quote = 0
			} else {
				quote = '"'
			}
		case rs[i] == '`':
			j := i + 1
			for j < len(rs) && rs[j] != '`' {
				j++
			}
			out = append(out, string(rs[i+1:j]))
			i = j
		case (rs[i] == '$' || rs[i] == '<' || rs[i] == '>') && i+1 < len(rs) && rs[i+1] == '(':
			// $(…) — подстановка команды, <(…) и >(…) — подстановка процесса
			depth, j := 1, i+2
			for j < len(rs) && depth > 0 {
				if rs[j] == '(' {
					depth++
				} else if rs[j] == ')' {
					depth--
				}
				j++
			}
			out = append(out, string(rs[i+2:j-1]))
			i = j - 1
		}
	}
	return out
}

// mutatingKey — читающая по имени команда с ключом, который меняет сервер:
// «find -delete», «sort -o», «sysctl -w», «ip link set», «mount -o»,
// «awk system(…)». Это не справочник, а страховка уровня sudo и
// перенаправления: образец из круга чтения ловит имя команды, а не ключ.
func mutatingKey(part string) string {
	word := firstWord(part)
	markers := map[string][]string{
		"find":   {"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fls"},
		"sort":   {"-o", "--output"},
		"sysctl": {"-w", "--write", "-p", "--load"},
		"ip":     {" set ", " add ", " del ", " delete ", " flush ", " replace ", " change "},
		"mount":  {"-o", "--options", "/dev/", "--bind", "-t "},
		"umount": {""},
		"awk":    {"system(", "|&"},
		"gawk":   {"system(", "|&"},
		"sed":    {"-i", "--in-place", " w "},
		"xargs":  {""},
		"tee":    {""},
	}
	for _, m := range markers[word] {
		if m == "" || strings.Contains(part+" ", m) {
			return "«" + word + "» с изменяющим ключом: " + strings.TrimSpace(m)
		}
	}
	return ""
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

// SSHOrigin — кто просит команду: чат (с номером), модуль, задача.
type SSHOrigin struct {
	Who    string // «чат «Серверы»», «модуль «Серверы» · задача «Проверка дисков»»
	ChatID int64
	Hosts  []string // выданные серверы (nil — все: чат Сёмы)
	// External — намерение пришло снаружи: письмо, чужой текст.
	// Полномочий такое не даёт: суд его не разбирает, решает человек.
	External bool
	// Quiet — итог уйдёт одной записью в конце работы: так ночная смена
	// не превращает ночь в поток уведомлений, которые никто не читает.
	// Беда молчания не знает: тревога уходит всегда.
	Quiet bool
}

func (o SSHOrigin) allowed(host string) bool {
	if o.Hosts == nil {
		return true
	}
	for _, h := range o.Hosts {
		if h == host {
			return true
		}
	}
	return false
}

// SSHOutcome — что вышло из запроса: выполнено, ждёт, отказано.
type SSHOutcome struct {
	Policy    string
	Why       string
	PendingID int64
	// NoRuleID — команда запрещена по умолчанию, потому что ни одно правило
	// сервера не подошло, и человеку отправлен запрос на правило. Не ноль —
	// у карточки две кнопки («Спрашивать для неё», «Разрешить её»). Запрет
	// по правилу «Запретить» номера не имеет: кнопок у него нет
	NoRuleID int64
	Output   string
	Code     int
	Duration time.Duration
	Err      string
	// Unreachable — до сервера не дошли (sshx.ErrUnreachable): соединение не
	// установилось. Остальные команды на тот же адрес упадут так же, поэтому
	// тот, кто гонит их пачкой (ночной осмотр), на этом останавливается.
	Unreachable bool
}

// RequestSSH — единая точка входа: правила сервера, права, выполнение или
// очередь. Правила — у каждого сервера свои: модуль, скрипт, чат и задача
// идут этим же путём и получают тот же ответ.
func (e *Engine) RequestSSH(ctx context.Context, o SSHOrigin, host, cmd string) SSHOutcome {
	cmd = strings.TrimSpace(cmd)
	out := SSHOutcome{}
	h, ok := e.store.Host(host)
	if !ok || !o.allowed(host) {
		out.Policy, out.Err = PolicyDeny, fmt.Sprintf("сервер %q не выдан — выдать можно на вкладке «Серверы»", host)
		e.store.LogSSH(store.SSHLogEntry{Host: host, Cmd: cmd, Who: o.Who, Decision: store.SSHDenied, Output: "сервер не выдан"})
		return out
	}
	if cmd == "" {
		out.Err = "пустая команда"
		return out
	}
	ruling := e.Decide(host, cmd)
	out.Policy, out.Why = ruling.Policy, ruling.Why
	switch out.Policy {
	case PolicyDeny:
		switch {
		case ruling.NoSudo:
			// «Без sudo» у сервера сильнее любого правила и сильнее
			// подтверждения: такую команду не показываем даже кнопкой
			out.Err = "команда с sudo отклонена: " + out.Why
		case ruling.Default:
			// ни одно правило не подошло: запрещено, но человек может завести
			// правило. Сёма сам правил не заводит — только кнопка человека
			out.NoRuleID = e.recordNoRule(o, h, cmd, ruling)
			out.Err = fmt.Sprintf("на сервере «%s» нет правила для этой команды — она не выполнена. "+
				"Человеку отправлена карточка с кнопками «Спрашивать для неё» и «Разрешить её»; не повторяйте команду и не обходите запрет другой", host)
		default:
			out.Err = "команда запрещена правилом сервера — Сёма такое не выполняет: " + out.Why
			// работа не сорвалась, но глупость не случилась — это отдельный
			// счёт, который не виден в исходах «удалось / не удалось»
			if o.ChatID > 0 {
				_ = e.store.MarkSkillSaved(o.ChatID, "отказ по правилу «Запретить»: "+clip(cmd, 120))
			}
		}
		e.store.LogSSH(store.SSHLogEntry{Host: host, Cmd: cmd, Who: o.Who, Decision: store.SSHDenied, Output: out.Why})
		return out
	case PolicyAsk:
		// сервер под самостоятельным управлением: команда идёт на суд двух
		// моделей, а не сразу человеку. Отказ суда — обычная карточка.
		if ok, why := e.courtAllowed(h, cmd, o); ok {
			court := e.Court(ctx, h, cmd, o)
			switch court.Decision {
			case VerdictRun:
				return e.runByCourt(ctx, h, cmd, o, court)
			case VerdictReject:
				out.Err = "суд отклонил: " + court.Why
				e.store.LogSSH(store.SSHLogEntry{Host: host, Cmd: cmd, Who: o.Who,
					Decision: store.SSHRejected, Output: "судья отклонил: " + court.Why})
				if o.ChatID > 0 {
					_ = e.store.MarkSkillSaved(o.ChatID, "суд отклонил: "+clip(court.Why, 120))
				}
				return out
			default:
				out.Why = "суд передал человеку: " + court.Why
			}
		} else if why != "" {
			out.Why = why
		}
		// та же команда на том же сервере уже ждёт человека — не плодим вторую
		if p, ok := e.pendingSame(host, cmd); ok {
			out.PendingID = p.ID
			if p.ChatID == 0 && o.ChatID != 0 {
				_ = e.store.SetSSHPendingChat(p.ID, o.ChatID)
			}
			e.ensurePendingNotice(p, out.Why)
			return out
		}
		id, err := e.store.AddSSHPending(&store.SSHPending{Host: host, Cmd: cmd, Who: o.Who, ChatID: o.ChatID})
		if err != nil {
			out.Err = err.Error()
			return out
		}
		out.PendingID = id
		e.store.LogSSH(store.SSHLogEntry{Host: host, Cmd: cmd, Who: o.Who, Decision: store.SSHWaiting, Output: out.Why})
		e.notifySSHPending(&store.SSHPending{ID: id, Host: host, Cmd: cmd, Who: o.Who, ChatID: o.ChatID}, out.Why)
		return out
	}
	res, err := e.executeSSH(ctx, h, cmd, o.Who)
	out.Output, out.Code, out.Duration = res.Output, res.Code, res.Duration
	if err != nil {
		out.Err = err.Error()
		var down *sshx.ErrUnreachable
		out.Unreachable = errors.As(err, &down)
	}
	e.guessHostKind(h, cmd, res.Output, err == nil)
	return out
}

// guessHostKind — род сервера по первому ответу: RouterOS не знает uname и
// отвечает «bad command name»; Linux называет себя. Правится на «Серверах».
func (e *Engine) guessHostKind(h *store.Host, cmd, output string, ok bool) {
	if h.Kind != "" {
		return
	}
	switch {
	case strings.Contains(output, "bad command name"), ok && strings.HasPrefix(cmd, "/"):
		h.Kind = store.HostRouterOS
	case strings.Contains(output, "Linux"), ok && !strings.HasPrefix(cmd, "/"):
		h.Kind = store.HostLinux
	default:
		return
	}
	if cur, found := e.store.Host(h.Name); found {
		cur.Kind = h.Kind
		_ = e.store.SaveHost(cur)
	}
}

// executeSSH выполняет команду: ключ берётся из хранилища ядром, обращение
// пишется в журнал секрета, вывод проходит маски, всё — в журнал команд.
func (e *Engine) executeSSH(ctx context.Context, h *store.Host, cmd, who string) (sshx.Result, error) {
	var res sshx.Result
	key, ok := e.store.SecretValue(h.Key)
	use := store.SecretUse{Name: h.Key, Who: "ядро · " + h.Name + " · " + clip(cmd, 60), Granted: ok}
	if !ok {
		use.Note = "ключа нет или не расшифровался"
		e.store.LogSecret(use)
		err := fmt.Errorf("ключ %q не найден в хранилище или не читается", h.Key)
		e.store.LogSSH(store.SSHLogEntry{Host: h.Name, Cmd: cmd, Who: who, Decision: store.SSHError, Output: err.Error()})
		h.LastErr = err.Error()
		e.saveHostSeen(h)
		return res, err
	}
	e.store.LogSecret(use)
	timeout := e.SSHTimeout()
	if v, ok := ctx.Value(sshTimeoutCtx{}).(int); ok && v > 0 {
		timeout = v
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	res, err := e.sshRun(cctx, h, key, cmd)
	res.Output = e.maskOutput(res.Output)
	if err != nil {
		var fp *sshx.ErrFingerprint
		if errors.As(err, &fp) {
			h.NewFP = fp.Seen
		}
		h.LastErr = err.Error()
		e.saveHostSeen(h)
		if h.NewFP != "" {
			e.notifyFingerprint(h)
		}
		e.store.LogSSH(store.SSHLogEntry{Host: h.Name, Cmd: cmd, Who: who, Decision: store.SSHError,
			Millis: res.Duration.Milliseconds(), Output: err.Error()})
		return res, err
	}
	if h.Fingerprint == "" && res.Fingerprint != "" {
		h.Fingerprint = res.Fingerprint // первый вход: запоминаем
	}
	h.LastErr, h.NewFP = "", ""
	e.saveHostSeen(h)
	e.store.LogSSH(store.SSHLogEntry{Host: h.Name, Cmd: cmd, Who: who, Decision: store.SSHExecuted, Code: res.Code,
		Millis: res.Duration.Milliseconds(), Size: len(res.Output), Output: headTail(res.Output, 2500, 1500)})
	return res, nil
}

// saveHostSeen — записать то, что узналось о сервере из соединения
// (отпечаток, последняя ошибка), поверх свежей записи. Целым снимком
// писать нельзя: пока команда шла, человек мог нажать стоп-кран, сменить
// окно или уровень самостоятельности — и снимок, сделанный до команды,
// молча вернул бы решение человека назад.
func (e *Engine) saveHostSeen(h *store.Host) {
	cur, ok := e.store.Host(h.Name)
	if !ok {
		_ = e.store.SaveHost(h)
		return
	}
	cur.Fingerprint, cur.LastErr, cur.NewFP = h.Fingerprint, h.LastErr, h.NewFP
	_ = e.store.SaveHost(cur)
}

// sshTransport — как команда попадает на сервер. В работе это sshx.Run;
// подменяется только в тестах: ночную смену иначе не проверить, не заведя
// живой сервер, а проверять её надо целиком — от осмотра до отчёта.
type sshTransport func(ctx context.Context, h *store.Host, cmd string) (sshx.Result, error)

func (e *Engine) sshRun(ctx context.Context, h *store.Host, key, cmd string) (sshx.Result, error) {
	if e.sshVia != nil {
		return e.sshVia(ctx, h, cmd)
	}
	return sshx.Run(ctx, sshx.Target{Addr: h.Addr, Port: h.Port, User: h.User}, key, h.Fingerprint, cmd, e.SSHMaxOutput())
}

// maskOutput заменяет строки, где сработала маска секрета, пометкой.
func (e *Engine) maskOutput(text string) string {
	lines := strings.Split(text, "\n")
	n := 0
	for i, l := range lines {
		if e.secretMask(l) != "" {
			lines[i] = "[скрыто маской]"
			n++
		}
	}
	if n > 0 {
		return strings.Join(lines, "\n") + fmt.Sprintf("\n(%d строк скрыто масками секретов)", n)
	}
	return text
}

// ApproveSSH выполняет ожидающую команду по решению человека; результат
// уходит в чат, откуда пришла команда.
func (e *Engine) ApproveSSH(ctx context.Context, id int64, addAllow bool) error {
	ctx = context.WithValue(ctx, sshTimeoutCtx{}, e.SSHConfirmTimeout())
	// «Запретить» не обходится кнопкой: правила могли смениться, пока
	// команда ждала (запрет вписали, sudo выключили), и решение человека
	// на старой карточке сильнее не становится
	if p, err := e.store.SSHPendingByID(id); err == nil {
		if r := e.Decide(p.Host, p.Cmd); r.Policy == PolicyDeny {
			e.CloseNotification(sshNotifyKey(p.Host, p.Cmd), store.NotifyDismissed, "запрещено правилами сервера")
			e.decideTwins(id, p, store.SSHRejected)
			_ = e.store.DecideSSHPending(id, store.SSHRejected)
			e.store.LogSSH(store.SSHLogEntry{Host: p.Host, Cmd: p.Cmd, Who: p.Who, Decision: store.SSHDenied,
				Output: "кнопка не отменяет запрет: " + r.Why})
			// команда ждала кнопки с прежних времён, когда без правила она шла
			// на подтверждение, а правила для неё так и не завели: она не
			// пропадает, а становится запросом на правило с теми же двумя кнопками
			if r.Default && !r.NoSudo {
				if h, ok := e.store.Host(p.Host); ok {
					e.recordNoRule(SSHOrigin{Who: p.Who, ChatID: p.ChatID}, h, p.Cmd, r)
					return fmt.Errorf("на сервере «%s» нет правила для этой команды, поэтому она больше не ждёт кнопки «Выполнить»: "+
						"решите, спрашивать для неё или разрешить, в «Нет правила»", p.Host)
				}
			}
			return fmt.Errorf("теперь это запрещено правилами сервера «%s»: %s", p.Host, r.Why)
		}
	}
	if p, err := e.store.SSHPendingByID(id); err == nil {
		e.CloseNotification(sshNotifyKey(p.Host, p.Cmd), store.NotifyDone, "выполнено")
		e.decideTwins(id, p, store.SSHExecuted)
	}
	p, err := e.store.SSHPendingByID(id)
	if err != nil {
		return err
	}
	h, ok := e.store.Host(p.Host)
	if !ok {
		return fmt.Errorf("сервера %q больше нет", p.Host)
	}
	if addAllow {
		// «впредь без вопроса» — правило «Разрешить» ЭТОМУ серверу, а не всем:
		// у каждого сервера свои правила. Команда целиком, а не первое слово:
		// «apt-get» открыл бы и remove. Неясный синтаксис, sudo и
		// перенаправление правилом «Разрешить» не становятся никогда — они
		// проходят только по «Спросить»
		if e.Decide(p.Host, p.Cmd).CanAllow() {
			_ = e.store.AddHostRule(p.Host, store.RuleAllow, strings.TrimSpace(p.Cmd))
		}
	}
	_ = e.store.DecideSSHPending(id, store.SSHExecuted)
	res, rerr := e.executeSSH(ctx, h, p.Cmd, p.Who+" · подтвердил человек")
	if p.ChatID > 0 {
		card := ToolCard{Title: p.Host + " · " + p.Cmd, Code: res.Output, Seconds: res.Duration.Seconds(),
			SSH: &SSHCard{Host: p.Host, Cmd: p.Cmd, Status: "executed", Code: res.Code, PendingID: id}}
		if rerr != nil {
			card.Err, card.SSH.Status = rerr.Error(), "error"
		}
		// одна команда — одна карточка: та, что ждала кнопки, становится
		// выполненной, а не появляется второй строкой ниже
		if !e.replaceSSHCard(p.ChatID, id, card) {
			_ = e.saveToolCard(p.ChatID, card)
		}
		if e.store.Setting(fmt.Sprintf("chat.expires.%d", p.ChatID), "") != "" {
			e.touchChatExpiry(p.ChatID) // чат команды живёт от последнего действия
		}
		e.continueChat(p.ChatID)
	}
	return rerr
}

func (e *Engine) RejectSSH(id int64) error {
	if p, err := e.store.SSHPendingByID(id); err == nil {
		e.CloseNotification(sshNotifyKey(p.Host, p.Cmd), store.NotifyDismissed, "отклонено")
		e.decideTwins(id, p, store.SSHRejected)
	}
	p, err := e.store.SSHPendingByID(id)
	if err != nil {
		return err
	}
	_ = e.store.DecideSSHPending(id, store.SSHRejected)
	e.store.LogSSH(store.SSHLogEntry{Host: p.Host, Cmd: p.Cmd, Who: p.Who, Decision: store.SSHRejected})
	if p.ChatID > 0 {
		card := ToolCard{Title: p.Host + " · " + p.Cmd, Result: "отклонено человеком",
			SSH: &SSHCard{Host: p.Host, Cmd: p.Cmd, Status: "rejected", PendingID: id}}
		if !e.replaceSSHCard(p.ChatID, id, card) {
			_ = e.saveToolCard(p.ChatID, card)
		}
	}
	return nil
}

// RejectAllPending — снять с очереди всё, что накопилось.
//
// Команда, пролежавшая неделю, перестаёт значить то, что значила:
// «sudo kill 694563» с номером процесса восьмидневной давности сегодня
// либо ничто, либо совсем другой процесс. Разбирать такой хвост по
// одной кнопке — не работа для человека, а очередь из сорока семи
// нажатий; поэтому снимаем разом, а свежие команды придут заново.
//
// Ничего не выполняется: каждая запись закрывается как отклонённая,
// уведомление снимается, решение уходит в журнал — видно, что было
// снято и когда.
func (e *Engine) RejectAllPending(host string) (int, error) {
	list, err := e.store.SSHPendingList()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range list {
		if host != "" && p.Host != host {
			continue
		}
		if err := e.RejectSSH(p.ID); err != nil {
			log.Printf("отклонить %d: %v", p.ID, err)
			continue
		}
		n++
	}
	return n, nil
}

// CheckHost — «Проверить связь»: безобидная команда, заодно первый вход// CheckHost — «Проверить связь»: безобидная команда, заодно первый вход
// запоминает отпечаток.
func (e *Engine) CheckHost(ctx context.Context, name string) (string, error) {
	h, ok := e.store.Host(name)
	if !ok {
		return "", fmt.Errorf("сервера %q нет", name)
	}
	res, err := e.executeSSH(ctx, h, "uname -a", "проверка связи")
	if err != nil {
		return "", err
	}
	// RouterOS отвечает «bad command name», Linux называет себя — так
	// определяется род сервера для подсказки модели
	e.guessHostKind(h, "uname -a", res.Output, res.Code == 0)
	return strings.TrimSpace(res.Output), nil
}

// pendingSame — та же команда на том же сервере уже ждёт решения.
func (e *Engine) pendingSame(host, cmd string) (*store.SSHPending, bool) {
	list, err := e.store.SSHPendingList()
	if err != nil {
		return nil, false
	}
	for _, p := range list {
		if p.Host == host && p.Cmd == cmd {
			return p, true
		}
	}
	return nil, false
}

// decideTwins закрывает двойников решённой команды: иначе они висят вечно и
// держат чат в ожидании человека, которого никто не зовёт.
func (e *Engine) decideTwins(id int64, p *store.SSHPending, decision string) {
	list, err := e.store.SSHPendingList()
	if err != nil {
		return
	}
	for _, other := range list {
		if other.ID == id || other.Host != p.Host || other.Cmd != p.Cmd {
			continue
		}
		_ = e.store.DecideSSHPending(other.ID, decision)
		log.Printf("ssh: команда %d закрыта вместе с %d (та же команда)", other.ID, id)
	}
}

// ensurePendingNotice — у ожидающей команды всегда есть открытое уведомление:
// иначе кнопка теряется, а чат ждёт решения, о котором никто не знает.
func (e *Engine) ensurePendingNotice(p *store.SSHPending, why string) {
	if _, open := e.store.OpenNotificationByKey(sshNotifyKey(p.Host, p.Cmd)); open {
		return
	}
	if why == "" {
		why = "ждёт подтверждения"
	}
	e.notifySSHPending(p, why)
}

// HealPendingNotices — потерянные кнопки: у каждой ожидающей команды снова
// есть уведомление. Зовётся пинателем чатов.
func (e *Engine) HealPendingNotices() int {
	list, err := e.store.SSHPendingList()
	if err != nil {
		return 0
	}
	n := 0
	for _, p := range list {
		if _, open := e.store.OpenNotificationByKey(sshNotifyKey(p.Host, p.Cmd)); open {
			continue
		}
		e.notifySSHPending(p, "ждёт подтверждения с "+whenShort(p.Created))
		// и в сам чат: кнопка должна быть там, где о ней говорили
		key := fmt.Sprintf("ssh.recard.%d", p.ID)
		if p.ChatID > 0 && e.store.Setting(key, "") == "" {
			_ = e.writeToolCard(p.ChatID, ToolCard{
				Title:  p.Host + " · " + p.Cmd,
				Result: "ждёт подтверждения с " + whenShort(p.Created),
				SSH:    &SSHCard{Host: p.Host, Cmd: p.Cmd, Status: "pending", PendingID: p.ID, Why: e.Decide(p.Host, p.Cmd).Why},
			})
			_ = e.store.SetSetting(key, "1")
		}
		n++
	}
	return n
}

// AcceptFingerprint принимает новый отпечаток: сервер переустановили.
func (e *Engine) AcceptFingerprint(name string) error {
	h, ok := e.store.Host(name)
	if !ok {
		return fmt.Errorf("сервера %q нет", name)
	}
	if h.NewFP == "" {
		return fmt.Errorf("нового отпечатка не видели")
	}
	h.Fingerprint, h.NewFP, h.LastErr = h.NewFP, "", ""
	e.CloseNotification("fp:"+name, store.NotifyDone, "принят новый отпечаток")
	log.Printf("ssh: принят новый отпечаток %s для %s", h.Fingerprint, name)
	return e.store.SaveHost(h)
}

// ---- модули ----

// sshTools — sema.ssh.run для модуля: только выданные серверы, те же круги.
func (e *Engine) sshTools(m *Module, who string) []script.Tool {
	return []script.Tool{{
		Name: "ssh.run", Need: "ssh", Args: "(host, cmd)",
		Desc: "команда на выданном сервере: чтение — сразу, ответ {output, code}; остальное ждёт подтверждения на «Серверах», ответ {pending, why}",
		Fn: func(a []any) (any, error) {
			o := SSHOrigin{Who: who, Hosts: m.GrantedHosts}
			if o.Hosts == nil {
				o.Hosts = []string{}
			}
			out := e.RequestSSH(context.Background(), o, script.Str(a, 0), script.Str(a, 1))
			if out.Err != "" {
				return nil, fmt.Errorf("%s", out.Err)
			}
			if out.PendingID > 0 {
				return map[string]any{"pending": out.PendingID, "why": out.Why}, nil
			}
			return map[string]any{"output": out.Output, "code": out.Code}, nil
		},
	}}
}

func sshStubTools() []script.Tool {
	return []script.Tool{{Name: "ssh.run", Need: "ssh", Args: "(host, cmd)",
		Desc: "серверы — только в модуле с выданными серверами; в скрипте из чата недоступно",
		Fn: func(a []any) (any, error) {
			return nil, fmt.Errorf("серверы доступны только модулям с выданными серверами: оформите это модулем")
		}}}
}

func (e *Engine) hostNamesWithKind() []string {
	hosts, _ := e.store.Hosts()
	var out []string
	for _, h := range hosts {
		kind := h.Kind
		if kind == "" {
			kind = "неизвестно"
		}
		out = append(out, h.Name+" ("+kind+")")
	}
	return out
}

// HostNames — имена серверов для подсказки модели и списков.
func (e *Engine) HostNames() []string {
	hosts, _ := e.store.Hosts()
	var out []string
	for _, h := range hosts {
		out = append(out, h.Name)
	}
	return out
}
