package selfcheck

// Двери две, описание одно — и это держится проверкой, а не обещанием.
//
// Обещание здесь уже подводило в соседнем месте: «единственная дверь»
// `mayGoOutside`, которую не звал ни один рабочий путь, и зелёный тест,
// засчитывавший за проверку комментарий. Поэтому здесь проверяется само
// построение: обе двери собираются из этого списка, а проверка, которой
// список не знает, не попадёт ни в одну.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/config"
)

func testEnv(t *testing.T) Env {
	t.Helper()
	c := &config.Config{KeyEnv: "SEMA_KEY"}
	c.Data = t.TempDir()
	c.DB = filepath.Join(c.Data, "sema.db")
	c.Listen = "127.0.0.1:0"
	return Env{Conf: c}
}

// Проверка, объявленная один раз, видна обеим дверям — и обе двери
// действительно её задают, а не перечисляют вопросы у себя.
func TestCheckVisibleToBothDoors(t *testing.T) {
	env := testEnv(t)
	startup := map[string]bool{}
	for _, r := range Collect(AtStartup, env) {
		startup[r.Name] = true
	}
	running := map[string]bool{}
	for _, r := range Collect(WhileRunning, env) {
		running[r.Name] = true
	}
	var shared int
	for _, c := range All() {
		if c.Look == nil {
			t.Errorf("проверка %q ничего не смотрит", c.Name)
		}
		if c.When&AtStartup == 0 && c.When&WhileRunning == 0 {
			t.Errorf("проверка %q не задаётся ни одной дверью — её нет вовсе", c.Name)
		}
		if c.When == Always {
			shared++
			if !startup[c.Name] || !running[c.Name] {
				t.Errorf("общая проверка %q досталась не обеим дверям: запуск=%v ход=%v",
					c.Name, startup[c.Name], running[c.Name])
			}
		}
	}
	if shared == 0 {
		t.Fatal("общих проверок нет — тогда двери и не сведены")
	}
	// та самая, ради которой всё затевалось: срок сертификата кончается на
	// работающем сервере, а спрашивал о нём только запуск
	if !running["Срок сертификата"] {
		t.Error("живой сервер не спрашивает про срок сертификата")
	}
}

// Проверка «на ходу» обязана пережить пустую среду: у живого сервера база
// есть, а у осмотра может не быть, и одна и та же функция ходит к обеим.
func TestSharedCheckToleratesEmptyEnv(t *testing.T) {
	env := testEnv(t) // базы нет нарочно
	for _, r := range Collect(Always, env) {
		if r.Name == "" {
			t.Fatal("итог без имени: его никто не сможет показать")
		}
		if r.Line == "" {
			t.Errorf("проверка %q промолчала — молчание неотличимо от «всё хорошо»", r.Name)
		}
		if (r.Trouble || r.Warn) && r.Line == "" {
			t.Errorf("проверка %q объявила беду, не сказав какую", r.Name)
		}
	}
}

// Ни одна дверь не заводит своих вопросов помимо списка: иначе они снова
// разъедутся молча.
func TestTwoDoorsOneList(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// дверь либо просит собрать ответы, либо перебирает список сама, чтобы
	// пропустить дорогой вопрос по сроку, — но список в обоих случаях этот
	doors := map[string][]string{
		"cmd/sema/doctor.go":                   {"Collect(selfcheck.AtStartup", "For(selfcheck.AtStartup"},
		"internal/engine/self_watch_doctor.go": {"Collect(selfcheck.WhileRunning", "For(selfcheck.WhileRunning"},
	}
	for file, calls := range doors {
		b, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		text := string(b)
		var asks bool
		for _, call := range calls {
			if strings.Contains(text, call) {
				asks = true
			}
		}
		if !asks {
			t.Errorf("%s не спрашивает общий список (нет ни одного из %q)", file, calls)
		}
		// признак своего списка: дверь сама решает, что беда, по имени
		// проверки, а не по её итогу
		if strings.Count(text, "Trouble: true") > 0 {
			t.Errorf("%s заводит свои вердикты — вопросы должны жить в selfcheck", file)
		}
	}
}
