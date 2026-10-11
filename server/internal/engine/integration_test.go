package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Интеграционные проверки: движок на временной базе, поддельная модель,
// сквозные сценарии. Сеть и настоящие модели не нужны.

// fakeProvider — модель, которая отвечает заранее заданным текстом или
// ошибкой. Считает, сколько раз её звали.
type fakeProvider struct {
	answer string
	err    error
	calls  int
	last   string
}

func (f *fakeProvider) Name() string { return "тестовая" }

func (f *fakeProvider) Complete(ctx context.Context, prompt string) (string, error) {
	f.calls++
	f.last = prompt
	started := time.Now()
	// как настоящий провайдер: отчитываемся в общий журнал
	llm.Report(ctx, "тестовая", "text", prompt, f.answer, llm.Usage{In: 10, Out: 5, Exact: true}, started, f.err)
	return f.answer, f.err
}

func (f *fakeProvider) CompleteStream(ctx context.Context, prompt string, onDelta func(string)) (string, error) {
	return f.Complete(ctx, prompt)
}

func (f *fakeProvider) Vision(ctx context.Context, prompt string, image []byte, mime string) (string, error) {
	return f.Complete(ctx, prompt)
}

// newTestEngine — движок на временной базе с одной поддельной моделью.
func newTestEngine(t testing.TB, model string, p llm.Provider) (*Engine, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	// не t.Setenv: тот запрещён в целях фаззинга, а движок читает ключ из среды
	old, had := os.LookupEnv("SEMA_KEY")
	_ = os.Setenv("SEMA_KEY", "ключ-для-тестов")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SEMA_KEY", old)
			return
		}
		_ = os.Unsetenv("SEMA_KEY")
	})
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("база: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// хранилище файлов — во временную папку: без этого storage.NewLocal
	// получает пустой путь и сыплет файлы в каталог пакета, оставляя
	// в репозитории папки вида «89/27/…»
	conf := &config.Config{Data: dir, KeyEnv: "SEMA_KEY"}
	conf.Files.Dir = filepath.Join(dir, "files")
	e, err := New(conf, st)
	if err != nil {
		t.Fatalf("движок: %v", err)
	}
	if p != nil {
		e.providers[model] = p
	}
	return e, st
}

// Сквозная проверка журнала отправленного: вызов модели через роль пишет
// строку с содержимым, направлением и признаком повтора.
func TestSentLogRecordsCall(t *testing.T) {
	fake := &fakeProvider{answer: `{"verdict": "keep", "why": "пригодится"}`}
	e, st := newTestEngine(t, "тестовая", fake)
	e.installUsage()
	roles := e.Roles()
	for i := range roles {
		if roles[i].Name == RoleMemory {
			roles[i].Model, roles[i].Spare, roles[i].Timeout = "тестовая", roleNoneName, 30
		}
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatalf("роли: %v", err)
	}

	ctx := llm.WithConsumer(context.Background(), "core:test")
	if _, _, err := e.AskRole(ctx, RoleMemory, "Разбери запись: письмо #85, счёт на оплату"); err != nil {
		t.Fatalf("запрос модели: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("модель звали %d раз", fake.calls)
	}
	rows, err := st.SentList(false, "", "", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("журнал отправленного: %d строк, ошибка %v", len(rows), err)
	}
	row := rows[0]
	if row.Consumer != "core:test" || row.Model != "тестовая" {
		t.Errorf("строка журнала: потребитель %q, модель %q", row.Consumer, row.Model)
	}
	if !strings.Contains(row.Preview, "письмо #85") {
		t.Errorf("выжимка не содержит запроса: %q", row.Preview)
	}
	if row.Class != store.ClassSensitive {
		t.Errorf("класс %q, ожидали чувствительное (в тексте счёт)", row.Class)
	}
	if !strings.Contains(row.Sources, "письма 85") {
		t.Errorf("источники %q", row.Sources)
	}
	if row.Retry {
		t.Error("вызов вне очереди помечен повтором")
	}
}

// Неудача внутри очереди — повтор, а не отказ: так «Здоровье» не краснеет
// из-за работы, которую переделают.
func TestQueueFailureCountsAsRetry(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.installUsage()

	ctx := llm.WithRetryable(llm.WithConsumer(context.Background(), "core:queue"))
	if _, _, err := e.AskRoleSpare(ctx, RoleMemory, "запрос", false); err == nil {
		t.Log("роль без модели вернула ошибку, как и ожидалось")
	}

	// прямая запись через колбэк: так же, как это делает провайдер
	llm.OnCall(llm.Call{Model: "тестовая", Consumer: "core:queue", Kind: "text",
		Prompt: "разбор записи", Err: context.DeadlineExceeded, Retryable: true})
	llm.OnCall(llm.Call{Model: "тестовая", Consumer: "chat:1", Kind: "text",
		Prompt: "вопрос человека", Err: context.DeadlineExceeded, Retryable: false})

	calls, retries, fails := st.ModelCallStat(time.Now().Add(-time.Hour))
	if calls < 2 || retries != 1 || fails != 1 {
		t.Fatalf("вызовов %d, повторов %d, отказов %d — ожидали 1 и 1", calls, retries, fails)
	}
}

// Очередь: работа не теряется, повторяется с паузой и уходит в «застряло»
// после предела попыток.
func TestQueueRetriesThenHolds(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.SetSetting("queue.max_tries", "2"); err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue("тестовая_работа", "42", "", 5); err != nil {
		t.Fatalf("постановка: %v", err)
	}
	// повторная постановка не создаёт вторую строку
	if err := st.Enqueue("тестовая_работа", "42", "", 9); err != nil {
		t.Fatal(err)
	}
	list, _ := st.WorkList("тестовая_работа", store.WorkWaiting, 10)
	if len(list) != 1 {
		t.Fatalf("в очереди %d работ, ожидали одну", len(list))
	}

	for i := 1; i <= 2; i++ {
		items, err := st.TakeWork(5, time.Minute)
		if err != nil || len(items) != 1 {
			t.Fatalf("взятие работы: %d, %v", len(items), err)
		}
		held, err := st.FailWork(items[0].ID, "модель не ответила", time.Now().Add(-time.Second), e.QueueMaxTries())
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 && !held {
			t.Fatal("после предела попыток работа должна уйти в «застряло»")
		}
	}
	stats, _ := st.WorkStats()
	if len(stats) != 1 || stats[0].Held != 1 {
		t.Fatalf("сводка очереди: %+v", stats)
	}
	// человек нажал «Повторить застрявшие»
	if n, err := st.RetryHeld("тестовая_работа"); err != nil || n != 1 {
		t.Fatalf("повтор застрявших: %d, %v", n, err)
	}
	stats, _ = st.WorkStats()
	if stats[0].Held != 0 || stats[0].Waiting != 1 {
		t.Fatalf("после повтора: %+v", stats)
	}
}

// Уведомление: тот же ключ заменяет прежнее открытое, история остаётся.
func TestNotificationReplacesByKey(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	for i := 1; i <= 3; i++ {
		if _, err := e.Notify(&store.Notification{Key: "server:test", Source: "core", Level: "warn",
			Title: "проверка " + strconv.Itoa(i)}); err != nil {
			t.Fatalf("уведомление: %v", err)
		}
	}
	open, _ := st.Notifications(true, 10)
	if len(open) != 1 || !strings.HasSuffix(open[0].Title, "3") {
		t.Fatalf("открытых %d, первое %q", len(open), open[0].Title)
	}
	done, _ := st.Notifications(false, 10)
	if len(done) != 2 {
		t.Fatalf("в истории %d записей, ожидали две", len(done))
	}
}

// Скилл из сборки ставится сам и не перезаписывается после правки человеком.
func TestBuiltinSkillsRespectHumanEdit(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.loadBuiltinSkills()
	k, ok := st.Skill("linux-admin")
	if !ok {
		t.Fatal("встроенный скилл не установлен")
	}
	k.Text = "мой текст"
	if err := e.SaveSkill(k, "Вадим"); err != nil {
		t.Fatalf("правка скилла: %v", err)
	}
	e.loadBuiltinSkills() // как при перезапуске сервера
	again, _ := st.Skill("linux-admin")
	if again.Text != "мой текст" {
		t.Error("правка человека затёрта обновлением из сборки")
	}
}

// Маски секретов: значение секрета не должно попасть в журнал отправленного.
func TestSecretNeverReachesSentLog(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.installUsage()
	if err := st.AddMemoryRule("mask", "sk-tainted", "тестовая маска"); err != nil {
		t.Skipf("маски недоступны: %v", err)
	}
	llm.OnCall(llm.Call{Model: "тестовая", Consumer: "core:test", Kind: "text",
		Prompt: "ключ sk-tainted-123 в тексте", Answer: "ок"})
	rows, _ := st.SentList(false, "", "", 5)
	if len(rows) == 0 {
		t.Fatal("журнал пуст")
	}
	if strings.Contains(rows[0].Prompt, "sk-tainted-123") || strings.Contains(rows[0].Preview, "sk-tainted-123") {
		t.Errorf("секрет попал в журнал: %q", rows[0].Preview)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// Обновление модуля из чата не расширяет права молча: новые примитивы ждут
// человека, модуль пока работает по старым.
func TestModuleUpdateDoesNotEscalateRights(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	first := &Module{Name: "проба", Title: "Проба", About: "тест", Needs: []string{"mail"},
		Code: "function run() { return 1 }"}
	if err := e.InstallModule(first); err != nil {
		t.Fatalf("установка: %v", err)
	}
	if err := e.EnableModule("проба", true); err != nil {
		t.Fatalf("включение: %v", err)
	}
	// та же модель прислала обновление, дописав себе выход в сеть
	again := &Module{Name: "проба", Title: "Проба", About: "тест", Needs: []string{"mail", "http"},
		Code: "function run() { return 2 }"}
	if err := e.InstallModule(again); err != nil {
		t.Fatalf("обновление: %v", err)
	}
	got, ok := e.Module("проба")
	if !ok {
		t.Fatal("модуль пропал")
	}
	for _, n := range got.Needs {
		if n == "http" {
			t.Fatal("модуль сам себе выдал право «http»")
		}
	}
	if len(got.PendingNeeds) != 1 || got.PendingNeeds[0] != "http" {
		t.Fatalf("новое право не показано человеку: %v", got.PendingNeeds)
	}
	if !got.Enabled {
		t.Error("модуль выключился, хотя старые права никто не отбирал")
	}
	// человек выдал
	if err := e.GrantModuleNeeds("проба", true); err != nil {
		t.Fatalf("выдача прав: %v", err)
	}
	after, _ := e.Module("проба")
	found := false
	for _, n := range after.Needs {
		if n == "http" {
			found = true
		}
	}
	if !found || len(after.PendingNeeds) != 0 {
		t.Fatalf("после выдачи права: %v, ждут %v", after.Needs, after.PendingNeeds)
	}
}

// Закрытый основной ящик закрыт и для старых записей, где имя ящика пустое.
func TestClosedPrimaryAccountCoversLegacyRows(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.conf.Mail.Name = "основной"
	conf := st.MailConf("основной")
	no := false
	conf.Outbound = &no
	if err := st.SaveMailConf("основной", conf); err != nil {
		t.Fatalf("настройки ящика: %v", err)
	}
	if err := st.SaveAccounts([]*store.Account{{Name: "основной", Email: "основной@пример.рф"}}); err != nil {
		t.Fatalf("список ящиков: %v", err)
	}
	closed := e.ClosedAccounts(true)
	if !closed["основной"] {
		t.Fatal("закрытый ящик не попал в список закрытых")
	}
	if !closed[""] {
		t.Fatal("старые записи основного ящика (пустое имя) остались открытыми для облака")
	}
}
