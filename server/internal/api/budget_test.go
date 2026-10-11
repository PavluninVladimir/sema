package api

// Бюджет экрана: до тысячи узлов и до ста килобайт на первом показе.
//
// Замер 12 сентября 2026: MAX — 2 094 узла и 207 КБ в свёрнутом виде,
// Telegram — 1 305 и 138, журнал настроек — 1 505, память — 2 228, файлы
// — 2 014. Ни одно из этих чисел не было видно ни в одном тесте: экраны
// проверялись на сборку, а не на вес. Здесь вес считается по настоящей
// разметке — той, что уезжает в браузер, — и сравнивается с порогом.
//
// На пустой базе порог строгий. С живой базой (SEMA_BUDGET_DB=копия)
// тест печатает таблицу по всем экранам и роняет только те, что за
// порогом: так видно, что чинить, до того как человек это увидит.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

const (
	budgetNodes = 1000
	budgetBytes = 100 * 1024
)

type screenWeight struct {
	Screen string
	Nodes  int
	Bytes  int
}

// weigh — узлы и байты разметки экрана, как она уйдёт в браузер.
func weigh(t *testing.T, s *Server, screen string) screenWeight {
	t.Helper()
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatalf("%s: %v", screen, err)
	}
	markup, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatalf("%s: разметка: %v", screen, err)
	}
	return screenWeight{Screen: screen, Nodes: nodeCount(string(markup)), Bytes: len(markup)}
}

func nodeCount(markup string) int {
	корень, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		return -1
	}
	n := 0
	var walk func(*html.Node)
	walk = func(у *html.Node) {
		if у.Type == html.ElementNode {
			n++
		}
		for д := у.FirstChild; д != nil; д = д.NextSibling {
			walk(д)
		}
	}
	walk(корень)
	return n
}

func TestScreenBudget(t *testing.T) {
	var s *Server
	живая := os.Getenv("SEMA_BUDGET_DB")
	if живая != "" {
		s = serverOnDBCopy(t, живая)
	} else {
		s, _ = testServer(t)
	}
	// граф памяти — единственный экран на всё окно, и его вес — сами узлы
	// и рёбра в SVG: «тяжёлое уезжает на свой экран» — это он и есть
	exceptions := map[string]string{"memory:graph": "граф на всё окно, вес — сам граф"}
	var веса []screenWeight
	seen := map[string]bool{}
	for _, screen := range append(append([]string{}, coreScreens...), allScreens...) {
		if seen[screen] || exceptions[screen] != "" {
			continue
		}
		seen[screen] = true
		веса = append(веса, weigh(t, s, screen))
	}
	sort.Slice(веса, func(i, j int) bool { return веса[i].Nodes > веса[j].Nodes })
	var таблица strings.Builder
	fmt.Fprintf(&таблица, "\n%-36s %7s %8s\n", "экран", "узлов", "КБ")
	for _, в := range веса {
		знак := " "
		if в.Nodes > budgetNodes || в.Bytes > budgetBytes {
			знак = "!"
		}
		fmt.Fprintf(&таблица, "%s%-35s %7d %8.1f\n", знак, в.Screen, в.Nodes, float64(в.Bytes)/1024)
	}
	t.Log(таблица.String())
	for _, в := range веса {
		if в.Nodes > budgetNodes {
			t.Errorf("%s: %d узлов при бюджете %d\n%s", в.Screen, в.Nodes, budgetNodes, parseWeight(t, s, в.Screen))
		}
		if в.Bytes > budgetBytes {
			t.Errorf("%s: %d КБ при бюджете %d\n%s", в.Screen, в.Bytes/1024, budgetBytes/1024, parseWeight(t, s, в.Screen))
		}
	}
}

// parseWeight — из чего сложился вес: узлы по блокам верхнего уровня. Без
// этого число говорит «много», но не говорит где.
func parseWeight(t *testing.T, s *Server, screen string) string {
	t.Helper()
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		return err.Error()
	}
	var b strings.Builder
	for _, к := range doc.Components {
		markup, err := web.RenderFragment([]web.Block{toBlock(к)})
		if err != nil {
			continue
		}
		name := к.Title
		if name == "" {
			name = к.Subtitle
		}
		if name == "" {
			name = "(" + к.Type + ")"
		}
		if n := nodeCount(string(markup)); n >= 60 || len(markup) >= 8*1024 {
			fmt.Fprintf(&b, "      %5d узлов %6.1f КБ  %s · детей %d\n", n, float64(len(markup))/1024, name, len(к.Children))
		}
	}
	return b.String()
}

// serverOnDBCopy — сервер поверх согласованной копии живой базы (снятой
// `.backup`). Ключ шифрования чужой: секреты не читаются, и это нормально
// — вес экрана от них не зависит. Копию тест не трогает: файл переписывается
// во временный каталог, чтобы миграции и журнал WAL не легли рядом с ней.
func serverOnDBCopy(t testing.TB, путь string) *Server {
	t.Helper()
	dir := t.TempDir()
	b, err := os.ReadFile(путь)
	if err != nil {
		t.Fatalf("копия базы: %v", err)
	}
	база := filepath.Join(dir, "копия.db")
	if err := os.WriteFile(база, b, 0o600); err != nil {
		t.Fatal(err)
	}
	old, had := os.LookupEnv("SEMA_KEY")
	_ = os.Setenv("SEMA_KEY", "ключ-для-тестов")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SEMA_KEY", old)
			return
		}
		_ = os.Unsetenv("SEMA_KEY")
	})
	st, err := store.Open(база)
	if err != nil {
		t.Fatalf("база: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	conf := &config.Config{Data: dir, KeyEnv: "SEMA_KEY"}
	conf.Files.Dir = filepath.Join(dir, "files")
	eng, err := engine.New(conf, st)
	if err != nil {
		t.Fatalf("движок: %v", err)
	}
	return New(eng)
}

// Дамп экрана с копии живой базы: SEMA_BUDGET_DB=копия SEMA_PAGE_OUT=файл
// SEMA_PAGE_SCREEN=экран — чтобы смотреть настоящую разметку, а не
// разметку пустой базы, когда бюджет не сходится.
func TestScreenDumpOnBackup(t *testing.T) {
	живая, куда := os.Getenv("SEMA_BUDGET_DB"), os.Getenv("SEMA_PAGE_OUT")
	if живая == "" || куда == "" {
		t.Skip("нужны SEMA_BUDGET_DB и SEMA_PAGE_OUT")
	}
	screen := os.Getenv("SEMA_PAGE_SCREEN")
	if screen == "" {
		screen = "tasks"
	}
	s := serverOnDBCopy(t, живая)
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	html, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(куда, []byte(html), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("экран %q: %d КБ → %s", screen, len(html)/1024, куда)
}

// Профиль «Здоровья» на копии живой базы: тёплый ответ занимал 242–265 мс
// (замер 12 сентября 2026). go test -run XXX -bench Здоровье -cpuprofile
// cpu.out, потом go tool pprof -top cpu.out.
func BenchmarkHealth(b *testing.B) {
	живая := os.Getenv("SEMA_BUDGET_DB")
	if живая == "" {
		b.Skip("нужна копия базы в SEMA_BUDGET_DB")
	}
	s := serverOnDBCopy(b, живая)
	if _, err := s.buildScreen("health", "", "", 0); err != nil { // прогрев
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.buildScreen("health", "", "", 0); err != nil {
			b.Fatal(err)
		}
	}
}
