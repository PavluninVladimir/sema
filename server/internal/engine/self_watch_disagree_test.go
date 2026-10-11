package engine

// Проверка «две записи спорят» — та, которой не было в тот день, когда
// четыре целые копии подряд объявили потерянными. Поэтому её тест начинается
// ровно с того случая: задача говорит «не уехала», а Диск отдаёт файл.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/yadisk"
)

// diskWithOneFile — Диск, который отвечает про один файл. Пустой размер
// означает «такого файла нет».
func diskWithOneFile(t *testing.T, name string, size int64) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if size <= 0 || !strings.Contains(r.URL.Query().Get("path"), name) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Не удалось найти запрошенный ресурс."}`))
			return
		}
		_ = json.NewEncoder(w).Encode(yadisk.Entry{Name: name, Size: size,
			Hash: "2b9f1e0c44aa77c3b2d1e5f60718293a4b5c6d7e8f90a1b2c3d4e5f607182930"})
	}))
	t.Cleanup(s.Close)
	return s
}

// readyDispute — движок с задачей копий, чей итог говорит то, что сказано.
func readyDispute(t *testing.T, taskResult string, onDiskByte int64) (*Engine, *store.Store) {
	t.Helper()
	e, st := newTestEngine(t, "тест", nil)
	if err := st.CreateTask(&store.Task{Title: "Копии наружу",
		System: WorkerBackup, Schedule: "каждый день в 03:30", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	task, err := st.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, z := range task {
		if z.System == WorkerBackup {
			if err := st.TaskRan(z.ID, taskResult); err != nil {
				t.Fatal(err)
			}
		}
	}
	host := diskWithOneFile(t, "сёма-2026-09-11-0330.tgz", onDiskByte)
	e.diskForTest = func() (*yadisk.Client, string, error) {
		return yadisk.NewForTests(host.URL, "проба"), "копии", nil
	}
	return e, st
}

// Итог говорит «не уехала», а Диск отдаёт файл — спор, о котором человек
// обязан узнать: иначе он полдня ищет пропавшую копию, которая на месте.
func TestOwnResultAgainstStorage(t *testing.T) {
	e, st := readyDispute(t,
		"копия сёма-2026-09-11-0330.tgz не уехала: EOF", 39_845_120)

	if n := e.checkDispute(context.Background()); n != 1 {
		t.Fatalf("спор не найден: %d", n)
	}
	findings, err := st.Findings(true, 10)
	if err != nil || len(findings) != 1 {
		t.Fatalf("находок %d (%v)", len(findings), err)
	}
	n := findings[0]
	if !strings.Contains(n.Summary, "сёма-2026-09-11-0330.tgz") {
		t.Errorf("в находке не названа копия: %s", n.Summary)
	}
	if !strings.Contains(n.Numbers, "МБ") || !strings.Contains(n.Numbers, "sha256") {
		t.Errorf("находка не показывает, чем измерено: %s", n.Numbers)
	}
	// эта проверка ходит в чужую сеть и потому не вправе ничего
	// останавливать — только сказать
	if n.Measure != store.MeasureAlert {
		t.Errorf("сверка с хранилищем поставила меру %q — она не должна", n.Measure)
	}
}

// Диск отвечает «такого файла нет» — итог задачи правдив, спорить не о чем.
// Молчание здесь важнее находки: проверка, шумящая при недоступной сети,
// научит не читать себя вовсе.
func TestBackupTruthIsNotADispute(t *testing.T) {
	e, st := readyDispute(t, "копия сёма-2026-09-11-0330.tgz не уехала: EOF", 0)

	if n := e.checkDispute(context.Background()); n != 0 {
		t.Fatalf("проверка нашла спор там, где обе стороны согласны: %d", n)
	}
	if findings, _ := st.Findings(true, 10); len(findings) != 0 {
		t.Fatalf("лишняя находка: %+v", findings[0])
	}
}

// Удачный итог не сверяется вовсе: спрашивать Диск о том, с чем никто не
// спорит, — лишний поход в сеть каждые пять минут.
func TestSuccessfulBackupNotVerify(t *testing.T) {
	e, _ := readyDispute(t, "копия сёма-2026-09-11-0330.tgz (38 МБ) уехала за 2m10s и сверена на Диске", 39_845_120)
	if n := e.checkDispute(context.Background()); n != 0 {
		t.Fatalf("сверяли то, о чём не спорят: %d", n)
	}
}

// Своё окно: в сеть эта проверка ходит раз в час, а не раз в пять минут.
func TestVerifyGoesInNetOnceInHour(t *testing.T) {
	e, _ := readyDispute(t, "копия сёма-2026-09-11-0330.tgz не уехала: EOF", 39_845_120)
	if n := e.checkDispute(context.Background()); n != 1 {
		t.Fatalf("первый заход должен найти спор: %d", n)
	}
	// вторая находка та же — записи не прибавится, но и в сеть ходить
	// незачем: метка времени должна удержать
	e2 := e.store.Setting(watchDisagreeWhen, "")
	if e2 == "" {
		t.Fatal("проверка не запомнила, когда ходила в сеть")
	}
	res, err := time.Parse(time.RFC3339, e2)
	if err != nil || time.Since(res) > time.Minute {
		t.Fatalf("метка времени похода не похожа на правду: %q", e2)
	}
	if n := e.checkDispute(context.Background()); n != 0 {
		t.Fatalf("проверка пошла в сеть второй раз подряд: %d", n)
	}
}

// Выключенная проверка не ходит никуда: это единственная из шести, которая
// зависит от чужой доступности, и её должно быть можно выключить.
func TestDisabledVerifySilent(t *testing.T) {
	e, _ := readyDispute(t, "копия сёма-2026-09-11-0330.tgz не уехала: EOF", 39_845_120)
	if err := e.SetWatchDisagree(false); err != nil {
		t.Fatal(err)
	}
	if n := e.checkDispute(context.Background()); n != 0 {
		t.Fatalf("выключенная проверка всё равно сходила: %d", n)
	}
}
