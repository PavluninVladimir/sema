package engine

// Ночная смена: сервер, отданный Сёме целиком (уровень «полная»), в своё
// окно обслуживается им самим. Порядок тот же, что у человека-администратора:
// сначала посмотреть, потом решить, потом делать по одному и проверять
// после каждого шага.
//
//  1. Осмотр — читающие команды из скилла администрирования по роду
//     сервера. Список правит человек в скилле, а не код: у домашнего
//     роутера и у сервера с почтой смотрят разное.
//  2. Наряд — роль «Арбитр» по осмотру пишет шаги: что, зачем, команда,
//     проба и возврат. Пустой наряд — нормальный итог: выдуманная ночью
//     работа хуже несделанной.
//  3. Шаги по одному — каждый через обычный суд двух моделей и runByCourt,
//     с новой проверкой окна, потолка и остановки перед каждым следующим.
//  4. Отчёт — одна запись в «Уведомления» в конце. Беда (проба не сошлась,
//     связь не вернулась) не ждёт отчёта: тревога уходит сразу, а сервер
//     останавливается до решения человека.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// WorkerNightShift — задача сердцебиения. По умолчанию выключена: пока
// человек сам не решил отдать сервер целиком, ночью ничего не происходит.
const WorkerNightShift = "night_shift"

const (
	nightMaxStepsKey    = "night.max_steps"      // шагов за смену
	nightLookMaxKey     = "night.look_max"       // читающих команд за осмотр
	nightLookTimeoutKey = "night.look_timeout_s" // предел времени на весь осмотр
	nightEveryKey       = "night.every_h"        // как часто, когда окно круглосуточное
	nightSkillKey       = "night.skill."         // + род сервера: имя скилла осмотра
	nightLastKey        = "night.last."          // + имя сервера: когда смена была
)

// NightStep — шаг наряда. Проба и возврат обязательны: без пробы нечем
// проверить, без возврата нечем отменить, а необратимое ночью не делают.
type NightStep struct {
	What  string `json:"what"`
	Why   string `json:"why"`
	Cmd   string `json:"cmd"`
	Probe string `json:"probe"`
	Back  string `json:"back"`
}

// NightShift — что вышло из смены на одном сервере: она же основа отчёта.
type NightShift struct {
	Host     string
	Looked   int      // сколько читающих команд осмотра выполнено
	Planned  int      // сколько шагов было в наряде
	Done     []string // выполнено самой, проба сошлась
	Human    []string // ждёт человека: возврат не назван, суд передал ему
	Skipped  []string // не делали и ждать нечего: круг «никогда», суд отклонил
	Deferred []string // отложено до следующей смены: окно, потолок, остановка
	Trouble  string   // беда: проба не сошлась, связь не вернулась
	Note     string   // почему смена не состоялась вовсе

	// Осмотр не удался: настоящая причина, а не «сервер молчит». Сервер жив,
	// а сломана бывает дорога к нему (порт 22 закрыл роутер), и человеку
	// надо видеть, что именно, не заглядывая в журнал команд.
	LookErr     string // ошибка, из-за которой осмотр не удался
	Unreachable bool   // до сервера не дошли: соединение не установилось
}

func (e *Engine) nightMaxSteps() int { return e.store.SettingInt(nightMaxStepsDef) }

// NightMaxSteps — то же для экрана: сколько шагов Сёма берёт за смену.
func (e *Engine) NightMaxSteps() int { return e.nightMaxSteps() }

// nightSkillName — скилл, из которого берутся команды осмотра. Настройка:
// человек может завести свой скилл под свой парк серверов.
func (e *Engine) nightSkillName(kind string) string {
	if kind == store.HostRouterOS {
		return e.store.SettingText(nightSkillRouterDef)
	}
	return e.store.SettingText(nightSkillLinuxDef)
}

// ---- осмотр ----

var (
	// ограждения кода, где скилл вправду показывает команды: json с
	// примером отчёта — не команды, и в осмотр ему попадать незачем
	reSkillFence  = regexp.MustCompile("(?s)```(?:sh|bash|shell|console|text)?\n(.*?)```")
	reSkillInline = regexp.MustCompile("`([^`\n]+)`")
	reLookFence   = regexp.MustCompile("(?s)```sema-осмотр\n(.*?)```")
)

// skillLookBlock — свой блок осмотра в скилле: ограждение ```sema-осмотр.
// Метка нарочно точная, а не «заголовок со словом осмотр»: у скилла таких
// заголовков несколько, и разбор однажды возьмёт соседнее ограждение — у
// «Linux-сервера» так и вышло, в осмотр поехал пример отчёта в JSON.
func skillLookBlock(text string) string {
	m := reLookFence.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return m[1]
}

// skillCommands — команды, названные в скилле. Сначала свой блок осмотра,
// иначе всё, что скилл показывает кодом; лишнее отсеет круг чтения.
func skillCommands(text string) []string {
	if block := skillLookBlock(text); block != "" {
		return splitSkillLines(block)
	}
	var out []string
	for _, m := range reSkillFence.FindAllStringSubmatch(text, -1) {
		out = append(out, splitSkillLines(m[1])...)
	}
	for _, m := range reSkillInline.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

func splitSkillLines(block string) []string {
	var out []string
	for _, l := range strings.Split(block, "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "$ ")
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// nightLookCommands — читающие команды осмотра для сервера. «Разрешить»
// самого сервера здесь и охрана, и фильтр: из скилла берётся только то, что
// правила ЭТОГО сервера выполняют без вопроса. Команда Linux на RouterOS и
// любая команда на сервере без правил в осмотр не попадают.
func (e *Engine) nightLookCommands(h *store.Host) (skill string, cmds []string) {
	name := e.nightSkillName(h.Kind)
	k, ok := e.store.Skill(name)
	if !ok || !k.Enabled {
		return name, nil
	}
	max := e.store.SettingInt(nightLookMaxDef)
	seen := map[string]bool{}
	for _, cmd := range skillCommands(k.Text) {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" || seen[cmd] {
			continue
		}
		if policy, _ := e.Classify(h.Name, cmd); policy != PolicyAllow {
			continue
		}
		seen[cmd] = true
		cmds = append(cmds, cmd)
		if len(cmds) >= max {
			break
		}
	}
	return name, cmds
}

// nightLook — осмотр: выполняет читающие команды и складывает вывод в один
// текст для наряда. Предел времени общий на весь осмотр: ночь не резиновая,
// а зависшая команда не должна съесть окно. Не дошли до сервера — осмотр
// на этом кончается: остальные команды упали бы так же, и каждая ждала бы
// свой срок соединения (12 команд по 15 секунд — три минуты окна впустую).
// Ошибка возвращается наружу — первая, а если осмотр оборвала недоступность,
// то она: без этого отчёт говорил бы «сервер молчит» про сервер, до которого
// просто закрыта дорога.
func (e *Engine) nightLook(ctx context.Context, h *store.Host, cmds []string) (look string, done int, firstErr string, unreachable bool) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(e.store.SettingInt(nightLookTimeoutDef))*time.Second)
	defer cancel()
	var b strings.Builder
	who := "ночная смена · " + h.Name + " · осмотр"
	for _, cmd := range cmds {
		if cctx.Err() != nil {
			b.WriteString("\n(осмотр прерван по времени)\n")
			break
		}
		out := e.RequestSSH(cctx, SSHOrigin{Who: who, Quiet: true}, h.Name, cmd)
		b.WriteString("$ " + cmd + "\n")
		switch {
		case out.Err != "":
			b.WriteString("(не вышло: " + clip(out.Err, 200) + ")\n\n")
			if firstErr == "" {
				firstErr = out.Err
			}
			if out.Unreachable {
				b.WriteString("(осмотр прерван: нет связи с сервером)\n")
				return b.String(), done, out.Err, true
			}
		default:
			done++
			text := strings.TrimSpace(out.Output)
			if text == "" {
				text = "(пусто)"
			}
			b.WriteString(clip(text, 1200) + "\n\n")
		}
	}
	return b.String(), done, firstErr, false
}

// ---- наряд ----

const nightPlanPrompt = `Ты — администратор, которому владелец поручил ночное обслуживание его собственного сервера: это его домашнее оборудование и его серверы, доступ выдан им самим ключом, а ниже — вывод читающих команд осмотра, которые он разрешил. Никакого чужого оборудования, никакого проникновения: только уход за своим. Составь наряд работ по осмотру. Отвечай по-русски, только JSON:
{"steps": [{"what": "что сделать", "why": "зачем", "cmd": "одна команда",
  "probe": "читающая команда, по которой видно, что всё получилось",
  "back": "команда, которая вернёт сделанное назад"}]}

Правила:
- Наряд — обслуживание того, что уже есть: обновления, чистка места, перезапуск упавшей службы.
  Ничего нового не ставится и не настраивается.
- Один шаг — одна команда в одну строку: без sh -c, без подстановок $(…) и без записи вывода в файл.
- Проба и возврат обязательны у каждого шага. Вернуть нечем — такого шага в наряде быть не должно.
- Работы не нашлось — верни пустой список шагов. Это нормальный итог, выдумывать работу не надо.
- Не больше %d шагов, самые нужные первыми.

Сервер: %s (род %s)

Скилл администратора:
%s

Что показал осмотр:
%s`

// nightPlan — наряд по осмотру. Пустой наряд — не ошибка.
func (e *Engine) nightPlan(ctx context.Context, h *store.Host, skillText, look string) ([]NightStep, error) {
	kind := h.Kind
	if kind == "" {
		kind = "неизвестен"
	}
	prompt := fmt.Sprintf(nightPlanPrompt, e.nightMaxSteps(), h.Name, kind, clip(skillText, 6000), clip(look, 12000))
	answer, _, err := e.askRoleGuarded(llm.WithConsumer(ctx, "core:night_shift"), RoleArbiter, prompt, true)
	if err != nil {
		return nil, err
	}
	raw := extractJSON(answer)
	var wrap struct {
		Steps []NightStep `json:"steps"`
	}
	if json.Unmarshal([]byte(raw), &wrap) == nil {
		return wrap.Steps, nil
	}
	var list []NightStep
	if json.Unmarshal([]byte(raw), &list) == nil {
		return list, nil
	}
	return nil, fmt.Errorf("наряд не разобран: арбитр ответил не по форме")
}

// nightStepFault — почему шаг не берётся в работу. Пусто — можно.
func nightStepFault(s NightStep) string {
	switch {
	case strings.TrimSpace(s.Cmd) == "":
		return "шаг без команды"
	case strings.TrimSpace(s.Probe) == "":
		return "нечем проверить: проба не названа"
	case strings.TrimSpace(s.Back) == "":
		return "вернуть нечем"
	}
	return ""
}

// ---- смена ----

// nightShiftPass — проход задачи: смена на каждом сервере, чья очередь.
func (e *Engine) nightShiftPass(ctx context.Context) string {
	hosts, err := e.store.Hosts()
	if err != nil {
		return "ошибка: " + err.Error()
	}
	var lines []string
	now := time.Now()
	for _, h := range hosts {
		if !h.Full() || h.Paused != "" {
			continue
		}
		// уровень перечитывается по-настоящему: «полную» могли выставить
		// раньше, чем у сервера выключили sudo или разошёлся суд
		if why := e.FullAutonomyDenied(h); why != "" {
			continue
		}
		if !AutoWindowOK(h.Window, now) || !e.nightDue(h, now) {
			continue
		}
		sh := e.nightShiftHost(ctx, h)
		_ = e.store.SetSetting(nightLastKey+h.Name, now.Format(time.RFC3339))
		e.nightReport(sh)
		lines = append(lines, sh.Line())
	}
	if len(lines) == 0 {
		return "серверов, чья смена сейчас, нет"
	}
	return strings.Join(lines, " · ")
}

// Line — итог смены одной строкой: она уходит в результат задачи.
func (s NightShift) Line() string {
	if s.Note != "" {
		return s.Host + ": " + s.Note
	}
	out := fmt.Sprintf("%s: осмотр %d, сделано %d", s.Host, s.Looked, len(s.Done))
	if len(s.Human) > 0 {
		out += fmt.Sprintf(", человеку %d", len(s.Human))
	}
	if len(s.Skipped) > 0 {
		out += fmt.Sprintf(", не делали %d", len(s.Skipped))
	}
	if len(s.Deferred) > 0 {
		out += fmt.Sprintf(", отложено %d", len(s.Deferred))
	}
	if s.Trouble != "" {
		out += ", беда: " + clip(s.Trouble, 120)
	}
	return out
}

// nightDue — пора ли: одна смена на одно окно. Окна нет (круглые сутки) —
// раз в night.every_h часов, иначе задача раз в пять минут работала бы
// сменой каждые пять минут.
func (e *Engine) nightDue(h *store.Host, now time.Time) bool {
	last, err := time.Parse(time.RFC3339, e.store.Setting(nightLastKey+h.Name, ""))
	if err != nil {
		return true // смены ещё не было
	}
	if strings.TrimSpace(h.Window) == "" {
		return now.Sub(last) >= time.Duration(e.store.SettingInt(nightEveryDef))*time.Hour
	}
	return last.Before(windowStart(h.Window, now))
}

// windowStart — когда началось нынешнее окно. Окно через полночь началось
// вчера, и смена в нём — одна на всю ночь, а не две по разные стороны суток.
func windowStart(window string, now time.Time) time.Time {
	from, _, ok := strings.Cut(strings.TrimSpace(window), "-")
	m, err := parseHourMin(from)
	if !ok || err != nil {
		return now // окно не разобрано: считаем, что смена уже была
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), m/60, m%60, 0, 0, now.Location())
	if start.After(now) {
		start = start.AddDate(0, 0, -1)
	}
	return start
}

// nightShiftHost — смена на одном сервере.
func (e *Engine) nightShiftHost(ctx context.Context, h *store.Host) NightShift {
	sh := NightShift{Host: h.Name}
	skill, cmds := e.nightLookCommands(h)
	if len(cmds) == 0 {
		sh.Note = "осмотр нечем сделать: в скилле «" + skill + "» нет читающих команд"
		return sh
	}
	look, done, lookErr, unreachable := e.nightLook(ctx, h, cmds)
	sh.Looked = done
	// Связь пропала посреди осмотра — наряд тоже не строится: он вышел бы по
	// половине осмотра, а шаги ушли бы по дороге, которая закрыта.
	if done == 0 || unreachable {
		sh.LookErr, sh.Unreachable = clip(lookErr, 300), unreachable
		switch {
		case unreachable && done == 0:
			// текст ошибки сам начинается с «нет связи с <адрес>»
			sh.Note = clip(lookErr, 200)
		case unreachable:
			sh.Note = "связь пропала посреди осмотра: " + clip(lookErr, 200)
		case lookErr != "":
			sh.Note = "команды осмотра не выполнились: " + clip(lookErr, 200)
		default:
			sh.Note = "осмотр прерван по времени: ни одна команда не выполнена"
		}
		return sh
	}
	text := ""
	if k, ok := e.store.Skill(skill); ok {
		text = k.Text
	}
	steps, err := e.nightPlan(ctx, h, text, look)
	if err != nil {
		sh.Note = "наряд не составлен: " + clip(err.Error(), 200)
		return sh
	}
	sh.Planned = len(steps)
	if max := e.nightMaxSteps(); len(steps) > max {
		for _, s := range steps[max:] {
			sh.Deferred = append(sh.Deferred, clip(s.Cmd, 100)+" — сверх потолка шагов за смену")
		}
		steps = steps[:max]
	}
	// холостая смена: наряд собран и показан, но ничего не выполняется.
	// Человек неделю смотрит, что Сёма собирался делать ночью, и по
	// этому решает, давать ли ему руки. Право давать нельзя, пока не
	// видно, чем оно распорядится
	if e.NightDryRun() {
		for _, s := range steps {
			sh.Deferred = append(sh.Deferred, clip(s.Cmd, 100)+" — смена вхолостую, ничего не выполняю")
		}
		return sh
	}
	e.nightRunSteps(ctx, h, steps, &sh)
	return sh
}

// NightDryRun — смена вхолостую: собрать наряд и показать, не выполняя.
// По умолчанию включено: право выполнять даётся человеком осознанно, а не
// достаётся вместе с обновлением сервера.
func (e *Engine) NightDryRun() bool { return e.store.Setting(nightDryKey, "1") == "1" }

// SetNightDryRun — переключатель на экране «Серверы».
func (e *Engine) SetNightDryRun(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting(nightDryKey, v)
}

const nightDryKey = "night.dry_run"

// nightRunSteps — шаги по одному. Перед каждым заново проверяются окно,
// потолок изменений в час, остановка и уровень: человек мог нажать
// стоп-кран, пока шёл предыдущий шаг.
func (e *Engine) nightRunSteps(ctx context.Context, h *store.Host, steps []NightStep, sh *NightShift) {
	who := "ночная смена · " + h.Name
	for i, s := range steps {
		cur, ok := e.store.Host(h.Name)
		switch {
		case !ok:
			sh.Deferred = append(sh.Deferred, nightRest(steps[i:], "сервер убран из справочника")...)
			return
		case !cur.Full():
			sh.Deferred = append(sh.Deferred, nightRest(steps[i:], "самостоятельность выключена")...)
			return
		case cur.Paused != "":
			sh.Deferred = append(sh.Deferred, nightRest(steps[i:], "сервер остановлен: "+clip(cur.Paused, 100))...)
			return
		case !AutoWindowOK(cur.Window, time.Now()):
			sh.Deferred = append(sh.Deferred, nightRest(steps[i:], "окно кончилось")...)
			return
		}
		if n := e.store.CountSSHChanges(h.Name, time.Now().Add(-time.Hour).Format(time.RFC3339)); n >= e.AutoMaxHour(cur) {
			sh.Deferred = append(sh.Deferred, nightRest(steps[i:], fmt.Sprintf("потолок изменений в час исчерпан (%d)", n))...)
			return
		}
		// шаг, который нечем отменить, не выполняется даже с согласия суда:
		// «вернуть нечем» — это и есть необратимое
		if why := nightStepFault(s); why != "" {
			sh.Human = append(sh.Human, clip(s.Cmd, 100)+" — "+why)
			e.nightAskHuman(cur, s, why)
			continue
		}
		out := e.RequestSSH(ctx, SSHOrigin{Who: who, Quiet: true}, h.Name, s.Cmd)
		switch {
		case out.Policy == PolicyDeny:
			sh.Skipped = append(sh.Skipped, clip(s.Cmd, 100)+" — запрещено правилами сервера: "+clip(out.Why, 120))
		case out.PendingID != 0:
			sh.Human = append(sh.Human, clip(s.Cmd, 100)+" — "+clip(out.Why, 150))
		case out.Err != "":
			// беда — это то, после чего сервер остановлен: не сошлась проба,
			// не вернулась связь, не завелась страховка. Отказ суда бедой не
			// считается: не сделали — и правильно, смена идёт дальше
			after, ok := e.store.Host(h.Name)
			if ok && after.Paused == "" {
				sh.Skipped = append(sh.Skipped, clip(s.Cmd, 100)+" — "+clip(out.Err, 200))
				continue
			}
			sh.Trouble = clip(s.Cmd, 100) + " — " + clip(out.Err, 300)
			e.nightEnsurePaused(h.Name, sh.Trouble)
			sh.Deferred = append(sh.Deferred, nightRest(steps[i+1:], "смена прервана бедой")...)
			return
		default:
			sh.Done = append(sh.Done, clip(s.What, 120)+" · "+clip(s.Cmd, 120))
		}
	}
}

func nightRest(steps []NightStep, why string) []string {
	var out []string
	for _, s := range steps {
		out = append(out, clip(s.Cmd, 100)+" — "+why)
	}
	return out
}

// nightAskHuman — шаг, до которого Сёма ночью не дотрагивается: он ложится
// человеку карточкой с объяснением, а не пропадает молча.
func (e *Engine) nightAskHuman(h *store.Host, s NightStep, why string) {
	cmd := strings.TrimSpace(s.Cmd)
	if cmd == "" {
		return
	}
	// кнопка «Выполнить» не сильнее правил сервера: запрещённое человеку не
	// показывается как «дождись кнопки», а где правила нет — идёт карточкой
	// «нет правила» с кнопками завести его
	who := "ночная смена · " + h.Name
	switch r := e.Decide(h.Name, cmd); {
	case r.Policy == PolicyDeny && r.Default:
		e.recordNoRule(SSHOrigin{Who: who}, h, cmd, r)
		return
	case r.Policy == PolicyDeny:
		return
	}
	if p, ok := e.pendingSame(h.Name, cmd); ok {
		e.ensurePendingNotice(p, why)
		return
	}
	id, err := e.store.AddSSHPending(&store.SSHPending{Host: h.Name, Cmd: cmd, Who: "ночная смена · " + h.Name})
	if err != nil {
		return
	}
	e.store.LogSSH(store.SSHLogEntry{Host: h.Name, Cmd: cmd, Who: "ночная смена · " + h.Name,
		Decision: store.SSHWaiting, Output: why + "\nзачем: " + clip(s.Why, 300)})
	e.notifySSHPending(&store.SSHPending{ID: id, Host: h.Name, Cmd: cmd, Who: "ночная смена · " + h.Name}, why)
}

// nightEnsurePaused — беда останавливает сервер. runByCourt делает это сам,
// но смена не полагается на чужую руку: остановка обязана случиться.
func (e *Engine) nightEnsurePaused(name, why string) {
	if h, ok := e.store.Host(name); ok && h.Paused != "" {
		return
	}
	e.PauseHost(name, "ночная смена: "+clip(why, 200))
}

// nightReport — одна запись по окончании смены. Всё, что случилось за ночь,
// собрано здесь; тревога о беде ушла раньше и отдельно.
func (e *Engine) nightReport(sh NightShift) {
	level, summary := "ok", "делать было нечего"
	switch {
	case sh.Trouble != "":
		level, summary = "warn", "смена прервана: "+clip(sh.Trouble, 200)
	case len(sh.Human) > 0 && len(sh.Done) == 0:
		level, summary = "warn", fmt.Sprintf("ждут человека: %d", len(sh.Human))
	case len(sh.Skipped) > 0 && len(sh.Done) == 0:
		level, summary = "info", fmt.Sprintf("не делали: %d", len(sh.Skipped))
	case sh.Note != "":
		level, summary = "warn", sh.Note
	case len(sh.Done) > 0:
		level = "info"
		summary = fmt.Sprintf("сделано %d", len(sh.Done))
		if len(sh.Deferred) > 0 {
			summary += fmt.Sprintf(", отложено %d", len(sh.Deferred))
		}
	}
	fields := []store.NotifyField{
		{Label: "Осмотр", Value: fmt.Sprintf("читающих команд: %d · шагов в наряде: %d", sh.Looked, sh.Planned)},
	}
	if sh.LookErr != "" {
		fields = append(fields, store.NotifyField{Label: "Причина", Value: sh.LookErr, Level: "warn"})
	}
	if sh.Unreachable {
		fields = append(fields, store.NotifyField{Label: "Что проверить",
			Value: "жив ли сервер и открыта ли сеть от машины Сёмы до его порта SSH: фильтр на роутере, файрвол"})
	}
	if len(sh.Done) > 0 {
		fields = append(fields, store.NotifyField{Label: "Сделано", Value: strings.Join(sh.Done, "\n")})
	}
	if len(sh.Human) > 0 {
		fields = append(fields, store.NotifyField{Label: "Ждёт человека", Value: strings.Join(sh.Human, "\n"), Level: "warn"})
	}
	if len(sh.Skipped) > 0 {
		fields = append(fields, store.NotifyField{Label: "Не делали", Value: strings.Join(sh.Skipped, "\n")})
	}
	if len(sh.Deferred) > 0 {
		fields = append(fields, store.NotifyField{Label: "Отложено", Value: strings.Join(sh.Deferred, "\n")})
	}
	if sh.Trouble != "" {
		fields = append(fields, store.NotifyField{Label: "Не сошлось", Value: sh.Trouble, Level: "alert"})
	}
	_, _ = e.Notify(&store.Notification{
		Key: "night:" + sh.Host, Source: "core", Level: level,
		Title:   sh.Host + " · ночная смена",
		Summary: summary,
		Fields:  fields,
		Actions: []store.NotifyAction{
			{Label: "Открыть журнал", Kind: "goto", Screen: ServerScreen(sh.Host, "log"), Style: "primary"},
			{Label: "Понял", Kind: "dismiss"},
		},
	})
}
