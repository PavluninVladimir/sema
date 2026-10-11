package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Разбор своего кода: функции, обращения, права. Проверяется на живых
// встроенных модулях — они и есть настоящий случай.

func TestParseModuleCode(t *testing.T) {
	code := `
function ensure() { return 1 }
function onMessage(msg) {
  var text = sema.files.text(msg.id);
  sema.data.put("receipts", "", {text: text});
  return sema.llm("разбери: " + text);
}
`
	c, err := ParseModuleCode(code)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if len(c.Funcs) != 2 {
		t.Fatalf("функций %d: %+v", len(c.Funcs), c.Funcs)
	}
	if f, ok := c.Func("onMessage"); !ok || f.Line != 3 {
		t.Errorf("onMessage на строке %d", f.Line)
	}
	want := map[string]bool{"files.text": true, "data.put": true, "llm": true}
	for _, u := range c.Uses {
		delete(want, u)
	}
	if len(want) != 0 {
		t.Errorf("не увидели обращения: %v (нашли %v)", want, c.Uses)
	}
}

// Синтаксическая ошибка ловится до установки, а не в рантайме.
func TestBrokenCodeRefused(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m := &Module{Name: "битый", Title: "Битый", About: "проба",
		Code: "function run() { var x = ; }"}
	if _, err := ParseModuleCode(m.Code); err == nil {
		t.Fatal("битый код разобрался")
	}
	if err := e.InstallModule(m); err == nil {
		t.Fatal("битый модуль установился")
	} else if !strings.Contains(err.Error(), "не разобран") {
		t.Errorf("причина отказа: %v", err)
	}
}

// Код зовёт то, чего не просил в манифесте — отказ до установки.
func TestMissingRightRefused(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m := &Module{Name: "жадный", Title: "Жадный", About: "проба", Needs: []string{"chat"},
		Code: `function run() { return sema.http.get("https://пример.рф"); }`}
	check, err := e.CheckModuleCode(m)
	if err != nil {
		t.Fatalf("проверка: %v", err)
	}
	if len(check.Missing) != 1 || check.Missing[0] != "http" {
		t.Fatalf("недостающие права: %v", check.Missing)
	}
	if !strings.Contains(check.Problem(), "не просил") {
		t.Errorf("объяснение модели: %q", check.Problem())
	}
	if err := e.InstallModule(m); err == nil {
		t.Fatal("модуль с невыданным правом установился")
	}
	// а с правом — устанавливается
	m.Needs = []string{"chat", "http"}
	if err := e.InstallModule(m); err != nil {
		t.Fatalf("честный модуль не установился: %v", err)
	}
}

// Право просят, а в коде не зовут — это подсказка, а не отказ.
func TestExtraRightIsHint(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m := &Module{Name: "запасливый", Title: "Запасливый", About: "проба",
		Needs: []string{"mail", "http"},
		Code:  `function run() { return sema.messages({limit: 1}); }`}
	check, err := e.CheckModuleCode(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(check.Extra) != 1 || check.Extra[0] != "http" {
		t.Errorf("лишние права: %v", check.Extra)
	}
	if check.Problem() != "" {
		t.Errorf("лишнее право стало отказом: %q", check.Problem())
	}
	if err := e.InstallModule(m); err != nil {
		t.Errorf("модуль не установился из-за подсказки: %v", err)
	}
}

// Видно, что отвалится: манифест ссылается на функцию, которой в новом
// коде нет.
func TestBrokenBounds(t *testing.T) {
	m := &Module{Name: "проба", Title: "Проба",
		Tasks:  []ModuleTask{{Title: "Утренний обход", Schedule: "9:00", Run: "morning()"}},
		On:     map[string]string{"message.added": "onMessage"},
		Blocks: map[string]string{"inbox": "block"},
	}
	code, err := ParseModuleCode("function onMessage() {}\nfunction block() {}\n")
	if err != nil {
		t.Fatal(err)
	}
	broken := Broken(m, code)
	if len(broken) != 1 || broken[0].Fn != "morning" || broken[0].What != "задача" {
		t.Fatalf("что отвалится: %+v", broken)
	}
	// а когда функция на месте, ничего не отваливается
	full, _ := ParseModuleCode("function morning() {}\nfunction onMessage() {}\nfunction block() {}\n")
	if len(Broken(m, full)) != 0 {
		t.Error("полный модуль сочтён сломанным")
	}
}

// Мёртвая функция: её не зовёт ни манифест, ни другая функция.
func TestDeadFuncs(t *testing.T) {
	m := &Module{Name: "проба", On: map[string]string{"message.added": "onMessage"}}
	code, err := ParseModuleCode(`
function onMessage() { return helper(); }
function helper() { return 1 }
function забытая() { return 2 }
`)
	if err != nil {
		t.Fatal(err)
	}
	dead := DeadFuncs(m, code)
	if len(dead) != 1 || dead[0] != "забытая" {
		t.Fatalf("мёртвые функции: %v", dead)
	}
}

// Оглавление короче кода: ради этого всё и затевалось.
func TestOutlineIsShorter(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m, ok := e.Module("receipts")
	if !ok {
		t.Skip("встроенного модуля «Чеки» нет")
	}
	code, err := ParseModuleCode(m.Code)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	outline := code.Outline()
	full := strings.Count(m.Code, "\n")
	short := strings.Count(outline, "\n")
	t.Logf("код %d строк, оглавление %d строк, функций %d", full, short, len(code.Funcs))
	if short >= full/2 {
		t.Errorf("оглавление не экономит: %d против %d", short, full)
	}
}

// Модели пишут функции стрелками и переменными: `const run = () => {}`.
// Такой модуль тоже должен разбираться, иначе его функции сочтутся
// пропавшими и манифест — сломанным.
func TestArrowAndVarFunctions(t *testing.T) {
	code := `
const refresh = () => { return sema.http.get("https://пример.рф"); };
var screen = function () { return {title: "Курсы"}; };
let helper = () => 1;
`
	c, err := ParseModuleCode(code)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	for _, want := range []string{"refresh", "screen", "helper"} {
		if _, ok := c.Func(want); !ok {
			t.Errorf("функция %q не найдена: %+v", want, c.Funcs)
		}
	}
	m := &Module{Name: "курсы",
		Tasks:   []ModuleTask{{Title: "Обновить", Schedule: "каждые 30 мин", Run: "refresh"}},
		Screens: []ModuleScreen{{ID: "rates", Title: "Курсы", Build: "screen"}}}
	if broken := Broken(m, c); len(broken) != 0 {
		t.Errorf("манифест сочтён сломанным: %+v", broken)
	}
}

// Инструмент объявляют и как «list», и как «receipts.list» — функция одна.
func TestToolNameWithPrefix(t *testing.T) {
	m := &Module{Name: "receipts", Tools: []string{"receipts.list(period) — выписка"}}
	code, err := ParseModuleCode("function list() { return [] }\n")
	if err != nil {
		t.Fatal(err)
	}
	if broken := Broken(m, code); len(broken) != 0 {
		t.Errorf("функция с префиксом не связалась: %+v", broken)
	}
	if dead := DeadFuncs(m, code); len(dead) != 0 {
		t.Errorf("функция инструмента сочтена мёртвой: %v", dead)
	}
}

// Право, которое даёт манифест (задачи по расписанию, свой экран), в коде
// не встречается — и это не повод считать его лишним.
func TestManifestRightsNotExtra(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m := &Module{Name: "пример", Title: "Пример", About: "проба",
		Needs:   []string{"schedule", "ui", "http"},
		Tasks:   []ModuleTask{{Title: "Раз в час", Schedule: "каждый час", Run: "run"}},
		Screens: []ModuleScreen{{ID: "s", Title: "Экран", Build: "screen"}},
		Code:    "function run() { return sema.http.get(\"https://пример.рф\") }\nfunction screen() { return {} }\n"}
	check, err := e.CheckModuleCode(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(check.Extra) != 0 {
		t.Errorf("права манифеста сочтены лишними: %v", check.Extra)
	}
}

// Сводка длинной сессии не должна вытеснять собственное начало: при
// переполнении она сворачивается, а не обрезается молча.
func TestSummaryFoldEnqueued(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	c, err := st.MemoryConsumer(store.ConsumerChat, "Сёма", "")
	if err != nil {
		t.Fatalf("потребитель: %v", err)
	}
	sid, err := st.StartMemorySession(c.ID, "проба", "")
	if err != nil {
		t.Fatal(err)
	}
	// короткая заметка свёртки не требует
	if err := e.noteAndFold(sid, "первая реплика"); err != nil {
		t.Fatal(err)
	}
	if st.HasWork(WorkSessionFold, "1") {
		t.Error("короткая сводка отправлена на свёртку")
	}
	// длинная — требует
	long := strings.Repeat("реплика про работу с кодом\n", 400)
	if err := e.noteAndFold(sid, long); err != nil {
		t.Fatal(err)
	}
	if !st.HasWork(WorkSessionFold, "1") && !st.HasWork(WorkSessionFold, "2") {
		t.Error("распухшая сводка не поставлена на свёртку")
	}
	s, _ := st.MemorySession(sid)
	if len([]rune(s.Summary)) > summaryKeep {
		t.Errorf("сводка длиннее предела: %d", len([]rune(s.Summary)))
	}
}

// Решение по черновику, которого больше нет, — не решение: молчать об
// этом нельзя, иначе человек увидит «принято», а скилла не будет.
func TestDecideMissingDraftFails(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.DecideSkillDraft("нет-такого", store.DraftAccepted, "Вадим"); err == nil {
		t.Error("решение по пропавшему черновику прошло молча")
	}
	if err := e.DecideDraft("нет-такого", true, "Вадим"); err == nil {
		t.Error("принятие пропавшего черновика прошло молча")
	}
}

// Принятие черновика: сначала скилл, потом отметка. Если скилл не
// сохранился, черновик остаётся открытым, и предложение можно повторить.
func TestAcceptSavesSkillFirst(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	d := &store.SkillDraft{ID: "п-1", Name: "linux-admin", Title: "Linux", Text: "Сначала apt-get update.", IsNew: true}
	if err := st.AddSkillDraft(d); err != nil {
		t.Fatal(err)
	}
	if err := e.DecideDraft("п-1", true, "Вадим"); err != nil {
		t.Fatalf("принятие: %v", err)
	}
	k, ok := st.Skill("linux-admin")
	if !ok || k.Text == "" {
		t.Fatal("скилл не сохранён")
	}
	got, _ := st.SkillDraft("п-1")
	if got.State != store.DraftAccepted {
		t.Errorf("состояние черновика: %q", got.State)
	}
}

// Модуль с sema.ssh.run ставится и обновляется. Право «ssh» — не
// примитив, а указание на список серверов: разбор манифеста вычищает его
// из needs в m.SSH, и проверка после этого требовала дописать то, что
// сама же и убирала. «Серверы под присмотром» из-за этого нельзя было
// обновить из чата с 3 сентября.
func TestModuleWithSSHInstalls(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m, err := ParseModule(`{"name":"проба-ssh","title":"Проба","ssh":["srv-bots"],
		"tasks":[{"title":"Обход","every":"1h","run":"обход"}]}
function обход() { return sema.ssh.run("srv-bots", "uptime"); }`)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := e.CheckModuleCode(m)
	if err != nil {
		t.Fatal(err)
	}
	if trouble := receipt.Problem(); trouble != "" {
		t.Fatalf("модуль с ssh не ставится: %s", trouble)
	}
	for _, n := range receipt.Missing {
		if n == "ssh" {
			t.Error("«ssh» снова требуют дописать в needs")
		}
	}
}

// Модуль, обёрнутый в (function () { … })(), не ставится: ядро зовёт
// задачу, инструмент и контекст по имени, а функции внутри обёртки снаружи
// не видны. 17 сентября 2026 модуль канала встал с «обновлён и работает»,
// а preview и run падали «is not defined».
func TestWrappedModuleNotInstalls(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m, err := ParseModule(`{"name":"проба-обёртка","title":"Проба","needs":["store"],
		"tasks":[{"title":"Подборка","schedule":"9:00, 19:00","run":"run"}],
		"tools":["preview() — превью"]}
(function () {
  function run() { return sema.store.get("x"); }
  function preview() { return 1; }
  return { run: run, preview: preview };
})();`)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := e.CheckModuleCode(m)
	if err != nil {
		t.Fatal(err)
	}
	trouble := receipt.Problem()
	if !strings.Contains(trouble, "run (задача)") || !strings.Contains(trouble, "preview (инструмент)") || !strings.Contains(trouble, "обёртку") {
		t.Fatalf("обёрнутый модуль прошёл проверку: %q", trouble)
	}
	if err := e.InstallModule(m); err == nil {
		t.Fatal("обёрнутый модуль установился")
	}
	// те же функции на верхнем уровне — ставится
	m.Code = "function run() { return sema.store.get(\"x\"); }\nfunction preview() { return 1; }"
	if receipt, _ := e.CheckModuleCode(m); receipt.Problem() != "" {
		t.Fatalf("правильный модуль не ставится: %s", receipt.Problem())
	}
}

// Блок модуля без манифеста получает понятный отказ, а не «invalid
// character 'r'» от скобки из кода; комментарий перед манифестом не мешает.
func TestModuleWithoutManifestClearDenial(t *testing.T) {
	_, err := ParseModule("/* «Технический форум» — подборка */\n\nvar CHANNEL = 'x';\nfunction run() { return { ok: true }; }")
	if err == nil || !strings.Contains(err.Error(), "не нашёл манифест") || !strings.Contains(err.Error(), "var CHANNEL") {
		t.Fatalf("отказ непонятный: %v", err)
	}
	m, err := ParseModule("// манифест\n{\"name\":\"проба\",\"needs\":[\"store\"]}\nfunction run() { return sema.store.get('x'); }")
	if err != nil || m.Name != "проба" {
		t.Fatalf("манифест после комментария не разобран: %v", err)
	}
}
