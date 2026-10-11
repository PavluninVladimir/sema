package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/sshx"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Ночная смена проверяется целиком — от осмотра до отчёта — на поддельной
// модели и подменённом транспорте SSH: живой сервер для этого трогать
// нельзя, а проверять рассуждением такое нельзя тем более.

// nightModel — модель, отвечающая по виду промпта: наряд, разбор, вердикт.
type nightModel struct {
	name  string
	reply func(prompt string) string
	calls int
}

func (m *nightModel) Name() string { return m.name }

func (m *nightModel) Complete(ctx context.Context, prompt string) (string, error) {
	m.calls++
	o := m.reply(prompt)
	llm.Report(ctx, m.name, "text", prompt, o, llm.Usage{In: 10, Out: 5, Exact: true}, time.Now(), nil)
	return o, nil
}

func (m *nightModel) CompleteStream(ctx context.Context, p string, on func(string)) (string, error) {
	return m.Complete(ctx, p)
}

func (m *nightModel) Vision(ctx context.Context, p string, img []byte, mime string) (string, error) {
	return m.Complete(ctx, p)
}

// nightSSH — подменённый транспорт: помнит порядок команд и отвечает так,
// как велит проверка.
type nightSSH struct {
	mu       sync.Mutex
	commands []string
	reply    func(cmd string) (sshx.Result, error)
}

func (f *nightSSH) run(_ context.Context, _ *store.Host, cmd string) (sshx.Result, error) {
	f.mu.Lock()
	f.commands = append(f.commands, cmd)
	f.mu.Unlock()
	if f.reply != nil {
		return f.reply(cmd)
	}
	return sshx.Result{Output: "ок", Code: 0}, nil
}

func (f *nightSSH) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func (f *nightSSH) index(cmd string) int {
	for i, c := range f.list() {
		if c == cmd {
			return i
		}
	}
	return -1
}

const nightAnalysis = `{"what":"перезапуск службы","reversible":true,"risk":"минута простоя",
 "probe":"systemctl is-active nginx","back":"systemctl start nginx"}`

// nightBench — сервер с полной самостоятельностью, суд из двух разных
// моделей, скилл со своим блоком осмотра и подменённый транспорт.
func nightBench(t *testing.T, order string) (*Engine, *store.Store, *nightSSH) {
	t.Helper()
	e, st := newTestEngine(t, "", nil)
	arbiter := &nightModel{name: "арбитр", reply: func(p string) string {
		if strings.Contains(p, `"steps"`) {
			return order // наряд работ по осмотру
		}
		return nightAnalysis
	}}
	judge := &nightModel{name: "судья", reply: func(string) string {
		return `{"decision":"run","why":"обратимо, проба и возврат названы"}`
	}}
	e.setProvider("арбитр", arbiter)
	e.setProvider("судья", judge)
	roles := e.Roles()
	for i := range roles {
		switch roles[i].Name {
		case RoleArbiter:
			roles[i].Model = "арбитр"
		case RoleJudge:
			roles[i].Model = "судья"
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	if ok, why := e.CourtReady(); !ok {
		t.Fatalf("суд не собрался: %s", why)
	}
	if err := st.SaveSkill(&store.Skill{Name: "linux-admin", Title: "Linux-сервер", Group: "Администрирование",
		Enabled: true, Source: store.SkillOwn,
		Text: "## Ежедневный осмотр\n\n```sema-осмотр\ndf -h\nuptime\n```\n\nПример отчёта:\n\n```json\n{\"level\": \"ok\"}\n```\n"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSecret(&store.Secret{Name: "ssh:проба", Kind: "ssh"}, "ключ-понарошку"); err != nil {
		t.Fatal(err)
	}
	h := &store.Host{Name: "проба", Addr: "127.0.0.1", Port: 22, User: "sema", Key: "ssh:проба",
		Kind: store.HostLinux, Autonomy: store.AutonomyFull, MaxHour: 5}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	// правила у сервера свои: набор Linux даёт осмотр и пробы, а то, что наряд
	// меняет, названо «Спросить» — без правила команда запрещена по умолчанию
	takeSet(t, e, "проба", store.RuleSetLinux)
	addRules(t, st, "проба", store.RuleAsk, "systemctl restart")
	fake := &nightSSH{}
	e.sshVia = fake.run
	// стенд проверяет выполнение: холостой режим для него выключаем
	// нарочно, а по умолчанию он включён
	if err := e.SetNightDryRun(false); err != nil {
		t.Fatal(err)
	}
	return e, st, fake
}

func testHost(t *testing.T, st *store.Store) *store.Host {
	t.Helper()
	h, ok := st.Host("проба")
	if !ok {
		t.Fatal("сервер пропал из справочника")
	}
	return h
}

// Наряд из двух шагов выполняется по одному: следующая команда уходит на
// сервер только после того, как у предыдущей сошлась проба. Пачкой такое
// судить нельзя — вердикт по пяти командам разом ничего не значит.
func TestNightShiftMakesStepsByOne(t *testing.T) {
	order := `{"steps":[
	 {"what":"перезапустить nginx","why":"служба упала","cmd":"systemctl restart nginx",
	  "probe":"systemctl is-active nginx","back":"systemctl stop nginx"},
	 {"what":"перезапустить cron","why":"задания не идут","cmd":"systemctl restart cron",
	  "probe":"systemctl is-active cron","back":"systemctl stop cron"}]}`
	e, st, fake := nightBench(t, order)

	result := e.nightShiftPass(context.Background())
	if !strings.Contains(result, "сделано 2") {
		t.Fatalf("смена не сделала оба шага: %q", result)
	}
	// осмотр взят из скилла и прошёл раньше наряда
	if fake.index("df -h") != 0 || fake.index("uptime") != 1 {
		t.Fatalf("осмотр не из скилла или не первым: %v", fake.list())
	}
	// второй шаг — только после пробы первого
	firstProbe := fake.index("systemctl is-active nginx")
	second := fake.index("systemctl restart cron")
	if firstProbe < 0 || second < 0 || second < firstProbe {
		t.Fatalf("шаги пошли не по одному: %v", fake.list())
	}
	// связь после изменения проверяется новым соединением
	if fake.index("echo sema-alive") < 0 {
		t.Fatalf("связь после изменения не проверялась: %v", fake.list())
	}
	// отчёт — одна запись, а не поток
	list, err := st.Notifications(true, 50)
	if err != nil {
		t.Fatal(err)
	}
	reports, stream := 0, 0
	for _, n := range list {
		switch {
		case n.Key == "night:проба":
			reports++
			if !strings.Contains(strings.Join(fields(n), "\n"), "перезапустить nginx") {
				t.Fatalf("в отчёте нет сделанного: %+v", n.Fields)
			}
		case strings.HasPrefix(n.Key, "auto-run:"):
			stream++
		}
	}
	if reports != 1 {
		t.Fatalf("отчётов о смене %d, а должен быть один", reports)
	}
	if stream != 0 {
		t.Fatalf("ночь превратилась в поток записей: %d", stream)
	}
	// вторая смена в том же окне не начинается
	if again := e.nightShiftPass(context.Background()); !strings.Contains(again, "нет") {
		t.Fatalf("смена пошла второй раз подряд: %q", again)
	}
	if h := testHost(t, st); h.Paused != "" {
		t.Fatalf("спокойная смена остановила сервер: %q", h.Paused)
	}
}

func fields(n *store.Notification) []string {
	var out []string
	for _, f := range n.Fields {
		out = append(out, f.Label+": "+f.Value)
	}
	return out
}

// Шаг, который нечем отменить, не выполняется: он ложится человеку с
// объяснением. «Вернуть нечем» — это и есть необратимое, а необратимое
// ночью не делают.
func TestNightShiftNotMakesStepWithoutRollback(t *testing.T) {
	order := `{"steps":[
	 {"what":"снести старые ядра","why":"место","cmd":"apt-get -y autoremove --purge",
	  "probe":"df -h /","back":""}]}`
	e, st, fake := nightBench(t, order)
	addRules(t, st, "проба", store.RuleAsk, "apt-get -y autoremove")
	sh := e.nightShiftHost(context.Background(), testHost(t, st))
	if len(sh.Done) != 0 {
		t.Fatalf("шаг без возврата выполнен: %+v", sh.Done)
	}
	if len(sh.Human) != 1 || !strings.Contains(sh.Human[0], "вернуть нечем") {
		t.Fatalf("шаг без возврата не ушёл человеку: %+v", sh.Human)
	}
	if fake.index("apt-get -y autoremove --purge") >= 0 {
		t.Fatalf("команда всё-таки ушла на сервер: %v", fake.list())
	}
	pending, err := st.SSHPendingList()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Cmd != "apt-get -y autoremove --purge" {
		t.Fatalf("человек не получил карточку: %+v", pending)
	}
}

// Шаг без возврата и без правила: человеку не дают кнопку «Выполнить» на то,
// что ни одно правило сервера не подпускает, — вместо неё карточка «нет
// правила» с кнопками завести правило. Кнопка не сильнее правил.
func TestNightShiftStepWithoutRuleAsksForRuleNotExecution(t *testing.T) {
	order := `{"steps":[
	 {"what":"снести старые ядра","why":"место","cmd":"apt-get -y autoremove --purge",
	  "probe":"df -h /","back":""}]}`
	e, st, fake := nightBench(t, order)
	sh := e.nightShiftHost(context.Background(), testHost(t, st))
	if len(sh.Done) != 0 || len(sh.Human) != 1 {
		t.Fatalf("шаг без возврата и без правила: %+v", sh)
	}
	if fake.index("apt-get -y autoremove --purge") >= 0 {
		t.Fatalf("команда ушла на сервер: %v", fake.list())
	}
	if pending, _ := st.SSHPendingList(); len(pending) != 0 {
		t.Fatalf("у запрещённого по умолчанию шага появилась кнопка «Выполнить»: %+v", pending)
	}
	if list := st.SSHNoRuleList(10); len(list) != 1 || list[0].Cmd != "apt-get -y autoremove --purge" {
		t.Fatalf("запроса на правило нет: %+v", list)
	}
}

// Осмотр смотрит только то, что разрешено правилами ЭТОГО сервера: команда из
// скилла, которой в «Разрешить» нет, в осмотр не попадает.
func TestNightLookOnlyAllowedByOwnRules(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SaveSkill(&store.Skill{Name: "linux-admin", Title: "Linux-сервер", Group: "Администрирование",
		Enabled: true, Source: store.SkillOwn,
		Text: "```sema-осмотр\ndf -h\nuptime\nfree -m\n```\n"}); err != nil {
		t.Fatal(err)
	}
	a := addHost(t, st, "а", store.HostLinux)
	b := addHost(t, st, "б", store.HostLinux)
	addRules(t, st, "а", store.RuleAllow, "df", "uptime")
	addRules(t, st, "б", store.RuleAllow, "free")
	_, got := e.nightLookCommands(a)
	if len(got) != 2 || got[0] != "df -h" || got[1] != "uptime" {
		t.Fatalf("осмотр сервера «а»: %v", got)
	}
	_, got = e.nightLookCommands(b)
	if len(got) != 1 || got[0] != "free -m" {
		t.Fatalf("осмотр сервера «б» смешался с правилами «а»: %v", got)
	}
	// сервер без правил не осматривается вовсе
	c := addHost(t, st, "в", store.HostLinux)
	if _, got := e.nightLookCommands(c); len(got) != 0 {
		t.Fatalf("у сервера без правил есть осмотр: %v", got)
	}
}

// Окно кончилось посреди наряда — остаток откладывается до следующей
// смены, а не доделывается за пределами окна.
func TestNightShiftPostponesBalanceForWindow(t *testing.T) {
	order := `{"steps":[
	 {"what":"перезапустить nginx","why":"упал","cmd":"systemctl restart nginx",
	  "probe":"systemctl is-active nginx","back":"systemctl stop nginx"},
	 {"what":"перезапустить cron","why":"молчит","cmd":"systemctl restart cron",
	  "probe":"systemctl is-active cron","back":"systemctl stop cron"}]}`
	e, st, fake := nightBench(t, order)
	// окно на три часа назад: пока шёл первый шаг, оно закрылось
	past := time.Now().Add(-3 * time.Hour)
	h := testHost(t, st)
	h.Window = past.Format("15:04") + "-" + past.Add(time.Hour).Format("15:04")
	fake.reply = func(cmd string) (sshx.Result, error) {
		if cmd == "systemctl restart nginx" {
			cur := testHost(t, st)
			cur.Window = h.Window
			if err := st.SaveHost(cur); err != nil {
				t.Error(err)
			}
		}
		return sshx.Result{Output: "ок"}, nil
	}
	sh := e.nightShiftHost(context.Background(), testHost(t, st))
	if len(sh.Done) != 1 {
		t.Fatalf("первый шаг не сделан: %+v", sh)
	}
	if len(sh.Deferred) != 1 || !strings.Contains(sh.Deferred[0], "окно кончилось") {
		t.Fatalf("остаток не отложен по окну: %+v", sh.Deferred)
	}
	if fake.index("systemctl restart cron") >= 0 {
		t.Fatalf("второй шаг выполнен за окном: %v", fake.list())
	}
}

// Проба не сошлась — сервер остановлен, тревога пришла сразу, остальные
// шаги не выполняются.
func TestNightShiftStopsHostOnFailedProbe(t *testing.T) {
	order := `{"steps":[
	 {"what":"перезапустить nginx","why":"упал","cmd":"systemctl restart nginx",
	  "probe":"systemctl is-active nginx","back":"systemctl stop nginx"},
	 {"what":"перезапустить cron","why":"молчит","cmd":"systemctl restart cron",
	  "probe":"systemctl is-active cron","back":"systemctl stop cron"}]}`
	e, st, fake := nightBench(t, order)
	fake.reply = func(cmd string) (sshx.Result, error) {
		if cmd == "systemctl is-active nginx" {
			return sshx.Result{Output: "inactive", Code: 3}, nil
		}
		return sshx.Result{Output: "ок"}, nil
	}
	sh := e.nightShiftHost(context.Background(), testHost(t, st))
	if sh.Trouble == "" {
		t.Fatalf("несошедшаяся проба не стала бедой: %+v", sh)
	}
	if len(sh.Done) != 0 {
		t.Fatalf("шаг с несошедшейся пробой засчитан сделанным: %+v", sh.Done)
	}
	if fake.index("systemctl restart cron") >= 0 {
		t.Fatalf("смена продолжилась после беды: %v", fake.list())
	}
	if h := testHost(t, st); h.Paused == "" {
		t.Fatal("сервер не остановлен после несошедшейся пробы")
	}
	list, err := st.Notifications(true, 50)
	if err != nil {
		t.Fatal(err)
	}
	alert := false
	for _, n := range list {
		if n.Level == "alert" && strings.Contains(n.Title, "проба") || n.Key == "auto:проба" {
			alert = true
		}
	}
	if !alert {
		t.Fatal("тревога о беде не пришла")
	}
	// следующий проход задачи такой сервер не берёт вовсе
	if result := e.nightShiftPass(context.Background()); !strings.Contains(result, "нет") {
		t.Fatalf("остановленный сервер снова пошёл в работу: %q", result)
	}
}

// Команды осмотра берутся из скилла, а не из кода: у настоящего скилла
// «Linux-сервер» они тоже находятся, и все — из круга чтения.
func TestLookTakenFromSkillAndOnlyReading(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.loadBuiltinSkills() // как при старте сервера
	h := &store.Host{Name: "проба", Kind: store.HostLinux}
	if err := st.SaveHost(h); err != nil {
		t.Fatal(err)
	}
	takeSet(t, e, "проба", store.RuleSetLinux)
	skill, cmds := e.nightLookCommands(h)
	if skill != "linux-admin" {
		t.Fatalf("скилл осмотра для linux: %q", skill)
	}
	if len(cmds) == 0 {
		t.Fatal("в скилле «Linux-сервер» не нашлось ни одной читающей команды осмотра")
	}
	for _, c := range cmds {
		if policy, why := e.Classify("проба", c); policy != PolicyAllow {
			t.Fatalf("в осмотр попала команда не из «Разрешить»: %q (%s, %s)", c, policy, why)
		}
	}
	// у роутера свой скилл, и команды из него — тоже читающие
	r := &store.Host{Name: "роутер", Kind: store.HostRouterOS}
	if err := st.SaveHost(r); err != nil {
		t.Fatal(err)
	}
	takeSet(t, e, "роутер", store.RuleSetRouterOS)
	if skill, cmds := e.nightLookCommands(r); skill != "mikrotik" || len(cmds) == 0 {
		t.Fatalf("осмотр роутера: скилл %q, команд %d", skill, len(cmds))
	}
}

// Окно у смены — одно на ночь: задача тикает раз в пять минут, а смена
// внутри окна должна случиться однажды.
func TestShiftOneOnWindow(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	now := time.Date(2026, 9, 9, 3, 0, 0, 0, time.Local)
	h := &store.Host{Name: "проба", Window: "02:00-06:00"}
	if !e.nightDue(h, now) {
		t.Fatal("первая смена в окне не началась")
	}
	_ = e.store.SetSetting(nightLastKey+h.Name, now.Add(-30*time.Minute).Format(time.RFC3339))
	if e.nightDue(h, now) {
		t.Fatal("вторая смена в том же окне")
	}
	// следующая ночь — снова можно
	if !e.nightDue(h, now.AddDate(0, 0, 1)) {
		t.Fatal("смена не началась следующей ночью")
	}
	// окно через полночь считается одной ночью, а не двумя
	night := &store.Host{Name: "проба", Window: "23:00-05:00"}
	_ = e.store.SetSetting(nightLastKey+night.Name,
		time.Date(2026, 9, 8, 23, 30, 0, 0, time.Local).Format(time.RFC3339))
	if e.nightDue(night, time.Date(2026, 9, 9, 1, 0, 0, 0, time.Local)) {
		t.Fatal("окно через полночь дало вторую смену за ночь")
	}
}

// Судья отклонил шаг — это не беда: сервер работает дальше, смена берётся
// за следующий шаг. Иначе одно «нет» судьи останавливало бы весь сервер до
// человека, а отказ — обычный ход суда, а не поломка.
func TestDenialCourtNotStopsShift(t *testing.T) {
	order := `{"steps":[
	 {"what":"перезапустить nginx","why":"упал","cmd":"systemctl restart nginx",
	  "probe":"systemctl is-active nginx","back":"systemctl stop nginx"},
	 {"what":"перезапустить cron","why":"молчит","cmd":"systemctl restart cron",
	  "probe":"systemctl is-active cron","back":"systemctl stop cron"}]}`
	e, st, fake := nightBench(t, order)
	// судья отклоняет первый шаг и соглашается со вторым
	denied := false
	e.setProvider("судья", &nightModel{name: "судья", reply: func(p string) string {
		if !denied && strings.Contains(p, "restart nginx") {
			denied = true
			return `{"decision":"reject","why":"службу трогать незачем"}`
		}
		return `{"decision":"run","why":"согласен"}`
	}})
	sh := e.nightShiftHost(context.Background(), testHost(t, st))
	if len(sh.Skipped) != 1 || !strings.Contains(sh.Skipped[0], "отклонил") {
		t.Fatalf("отказ суда не записан как «не делали»: %+v", sh)
	}
	if len(sh.Done) != 1 || !strings.Contains(sh.Done[0], "cron") {
		t.Fatalf("смена не дошла до второго шага: %+v", sh)
	}
	if sh.Trouble != "" {
		t.Fatalf("отказ суда сочтён бедой: %q", sh.Trouble)
	}
	if h := testHost(t, st); h.Paused != "" {
		t.Fatalf("отказ суда остановил сервер: %q", h.Paused)
	}
	if fake.index("systemctl restart nginx") >= 0 {
		t.Fatalf("отклонённая команда ушла на сервер: %v", fake.list())
	}
}

// Холостая смена: наряд собран и показан, но ни один шаг не выполнен.
// Право выполнять даётся человеком осознанно, а не достаётся вместе с
// включением задачи.
func TestShiftDryRunNothingNotMakes(t *testing.T) {
	order := `{"steps":[
	 {"what":"перезапустить nginx","why":"служба упала","cmd":"systemctl restart nginx",
	  "probe":"systemctl is-active nginx","back":"systemctl stop nginx"}]}`
	e, st, _ := nightBench(t, order)
	h := testHost(t, st)
	// стенд выключил холостой режим — возвращаем и проверяем умолчание
	if err := e.SetNightDryRun(true); err != nil {
		t.Fatal(err)
	}
	if !e.NightDryRun() {
		t.Fatal("холостой режим не включился")
	}
	sh := e.nightShiftHost(context.Background(), h)
	if sh.Planned == 0 {
		t.Fatal("наряд не собран — проверять нечего")
	}
	if len(sh.Done) != 0 {
		t.Fatalf("вхолостую выполнено шагов: %d", len(sh.Done))
	}
	if len(sh.Deferred) != sh.Planned {
		t.Fatalf("отложено %d из %d шагов", len(sh.Deferred), sh.Planned)
	}
}

// Умолчание проверяется на чистом движке, а не на стенде: право
// выполнять не должно доставаться вместе с включением задачи.
func TestDryRunModeEnabledByDefault(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if !e.NightDryRun() {
		t.Fatal("на чистой базе смена должна быть вхолостую")
	}
}

// nightNotice — отчёт ночной смены по серверу «проба» из «Уведомлений».
func nightNotice(t *testing.T, st *store.Store) *store.Notification {
	t.Helper()
	list, err := st.Notifications(true, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range list {
		if n.Key == "night:проба" {
			return n
		}
	}
	t.Fatal("отчёта о смене нет")
	return nil
}

// До сервера не дошли (роутер закрыл порт 22, а сервер жив): осмотр кончается
// на первой же команде, а не ждёт срока соединения у каждой из двенадцати, и
// отчёт называет настоящую причину. Раньше он говорил «сервер не отвечает
// читающим командам» и прятал ошибку в журнале команд, так что человек
// чинил сервер, а сломана была дорога к нему.
func TestNightLookStopsWhenUnreachable(t *testing.T) {
	e, st, fake := nightBench(t, `{"steps":[]}`)
	fake.reply = func(string) (sshx.Result, error) {
		return sshx.Result{}, &sshx.ErrUnreachable{Addr: "127.0.0.1:22",
			Err: errors.New("dial tcp 127.0.0.1:22: i/o timeout")}
	}
	sh := e.nightShiftHost(context.Background(), testHost(t, st))
	if n := len(fake.list()); n != 1 {
		t.Fatalf("после «нет связи» осмотр пробовал %d команд, а должен был одну: %v", n, fake.list())
	}
	if !sh.Unreachable {
		t.Fatalf("недоступность не отмечена: %+v", sh)
	}
	if !strings.Contains(sh.Note, "нет связи") || !strings.Contains(sh.Note, "i/o timeout") {
		t.Fatalf("причина в итоге смены названа не так: %q", sh.Note)
	}
	if !strings.HasPrefix(sh.Note, "нет связи с 127.0.0.1:22: ") {
		t.Fatalf("итог смены не начинается с текста ошибки соединения: %q", sh.Note)
	}
	if !strings.Contains(sh.Line(), "нет связи") {
		t.Fatalf("итог задачи не показывает причину: %q", sh.Line())
	}
	if h := testHost(t, st); h.Paused != "" {
		t.Fatalf("недоступный сервер остановлен как после беды: %q", h.Paused)
	}

	e.nightReport(sh)
	n := nightNotice(t, st)
	text := strings.Join(fields(n), "\n")
	if !strings.Contains(text, "Причина: ") || !strings.Contains(text, "i/o timeout") {
		t.Fatalf("в уведомлении нет ошибки: %s", text)
	}
	if !strings.Contains(text, "Что проверить: ") || !strings.Contains(text, "SSH") {
		t.Fatalf("в уведомлении нет подсказки, что проверить: %s", text)
	}
	if !strings.Contains(n.Summary, "нет связи") || n.Level != "warn" {
		t.Fatalf("итог уведомления: %q (%s)", n.Summary, n.Level)
	}
}

// Связь пропала посреди осмотра: наряд по половине осмотра не строится (арбитра
// не спрашивают), остальные команды не пробуются, а отчёт называет причину.
// Шаги по мёртвой дороге всё равно не выполнить.
func TestNightLookLostMidwayNoPlan(t *testing.T) {
	e, st, fake := nightBench(t, `{"steps":[]}`)
	if err := st.SaveSkill(&store.Skill{Name: "linux-admin", Title: "Linux-сервер", Group: "Администрирование",
		Enabled: true, Source: store.SkillOwn,
		Text: "```sema-осмотр\ndf -h\nuptime\nfree -m\n```\n"}); err != nil {
		t.Fatal(err)
	}
	arbiter := &nightModel{name: "арбитр", reply: func(string) string { return `{"steps":[]}` }}
	e.setProvider("арбитр", arbiter)
	fake.reply = func(cmd string) (sshx.Result, error) {
		if cmd == "uptime" {
			return sshx.Result{}, &sshx.ErrUnreachable{Addr: "127.0.0.1:22",
				Err: errors.New("dial tcp 127.0.0.1:22: i/o timeout")}
		}
		return sshx.Result{Output: "ок"}, nil
	}
	sh := e.nightShiftHost(context.Background(), testHost(t, st))
	if got := fake.list(); len(got) != 2 || got[0] != "df -h" || got[1] != "uptime" {
		t.Fatalf("после обрыва связи осмотр пошёл дальше: %v", got)
	}
	if arbiter.calls != 0 {
		t.Fatalf("наряд строился по неполному осмотру: арбитр спрошен %d раз", arbiter.calls)
	}
	if sh.Looked != 1 || sh.Planned != 0 || !sh.Unreachable {
		t.Fatalf("итог смены: %+v", sh)
	}
	if !strings.Contains(sh.Note, "связь пропала") || !strings.Contains(sh.Note, "i/o timeout") {
		t.Fatalf("в итоге смены нет причины: %q", sh.Note)
	}

	e.nightReport(sh)
	n := nightNotice(t, st)
	text := strings.Join(fields(n), "\n")
	if !strings.Contains(text, "Причина: ") || !strings.Contains(text, "i/o timeout") {
		t.Fatalf("в уведомлении нет ошибки: %s", text)
	}
	if !strings.Contains(text, "Что проверить: ") {
		t.Fatalf("в уведомлении нет подсказки, что проверить: %s", text)
	}
}

// Сервер ответил, но каждая команда осмотра упала по другой причине (ключ
// не принят, оболочка не открылась): дорога открыта, значит пробуются все
// команды, а в итоге стоит первая ошибка, без подсказки про сеть.
func TestNightLookAllFailReportsFirstError(t *testing.T) {
	e, st, fake := nightBench(t, `{"steps":[]}`)
	calls := 0
	fake.reply = func(string) (sshx.Result, error) {
		calls++
		return sshx.Result{}, fmt.Errorf("вход на 127.0.0.1:22 не удался: ошибка №%d", calls)
	}
	sh := e.nightShiftHost(context.Background(), testHost(t, st))
	if n := len(fake.list()); n != 2 {
		t.Fatalf("осмотр пробовал %d команд из двух: %v", n, fake.list())
	}
	if sh.Unreachable {
		t.Fatalf("обычная ошибка сочтена недоступностью: %+v", sh)
	}
	if !strings.Contains(sh.Note, "команды осмотра не выполнились") || !strings.Contains(sh.Note, "ошибка №1") {
		t.Fatalf("в итоге смены нет первой ошибки: %q", sh.Note)
	}
	if strings.Contains(sh.Note, "ошибка №2") {
		t.Fatalf("в итоге не первая ошибка, а последняя: %q", sh.Note)
	}

	e.nightReport(sh)
	text := strings.Join(fields(nightNotice(t, st)), "\n")
	if !strings.Contains(text, "Причина: ") || !strings.Contains(text, "ошибка №1") {
		t.Fatalf("в уведомлении нет ошибки: %s", text)
	}
	if strings.Contains(text, "Что проверить") {
		t.Fatalf("подсказка про сеть у сервера, до которого дошли: %s", text)
	}
}
