package api

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// healthLiveSized — данные размером с живую Сёму 3 октября 2026: 30 модулей
// с кодом (modules.json около 100 КБ), 50 моделей, 59 потребителей расхода
// (модули, чаты, ядро) и вызовы моделей за неделю.
func healthLiveSized(t *testing.T) *Server {
	t.Helper()
	s, st := testServer(t)
	code := "function run() { return 1; }\n" + strings.Repeat("// строка кода модуля, чтобы список весил как живой\n", 60)
	var consumers []string
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("mod%02d", i)
		if err := s.eng.InstallModule(&engine.Module{Name: name, Title: "Модуль " + name, Code: code}); err != nil {
			t.Fatal(err)
		}
		consumers = append(consumers, "module:"+name)
	}
	var models []string
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("model-%02d", i)
		base := "https://api.example.com/v1"
		if i%5 == 0 {
			base = "http://llm:8080/v1"
		}
		price := "$0.55 / $2.20 / 1M"
		if i%7 == 0 {
			price = ""
		}
		if err := s.eng.AddModel(engine.ModelConf{Name: name, Type: "openai", BaseURL: base, Model: name, Price: price}); err != nil {
			t.Fatal(err)
		}
		models = append(models, name)
	}
	for i := 0; i < 20; i++ {
		id, err := st.CreateChat(fmt.Sprintf("Чат номер %d", i))
		if err != nil {
			t.Fatal(err)
		}
		consumers = append(consumers, fmt.Sprintf("chat:%d", id))
	}
	for _, w := range []string{"poll", "archive", "attachments", "backfill", "folders", "purge", "memory", "module_events", "prices"} {
		consumers = append(consumers, "core:"+w)
	}
	now := time.Now()
	for i, c := range consumers {
		for j := 0; j < 5; j++ {
			_ = st.AddUsage(&store.UsageRow{At: now.Add(-time.Duration(i*5+j) * time.Hour), Consumer: c, Kind: "llm",
				Model: models[(i+j)%len(models)], TokensIn: 1000, TokensOut: 200, Seconds: 1.5, OK: true})
		}
		_ = st.AddUsage(&store.UsageRow{At: now.Add(-time.Duration(i) * time.Hour), Consumer: c, Kind: "run", Seconds: 0.2, OK: true})
	}
	return s
}

// Замер «Здоровья» на данных живого размера (perf-pass 3). 3 октября 2026 на
// Pi оно строилось 2,1 с. Цель на Pi — меньше 200 мс. Здесь бюджет грубый —
// 500 мс на лучшую из трёх сборок: под -race и на занятой машине точнее не
// мерить, а возврат к секундам он поймает.
func TestHealthScreenCostOnLiveSizedData(t *testing.T) {
	s := healthLiveSized(t)
	measure := func() (time.Duration, int) {
		tr := store.TraceSQL()
		defer tr.Stop()
		start := time.Now()
		if _, err := s.buildScreen("health", "", "", 0); err != nil {
			t.Fatal(err)
		}
		return time.Since(start), tr.Stats().N
	}
	first, q1 := measure()
	var best time.Duration
	var q int
	for i := 0; i < 3; i++ {
		d, n := measure()
		if best == 0 || d < best {
			best, q = d, n
		}
	}
	t.Logf("«Здоровье»: первая сборка %.1f мс (%d запросов), лучшая из трёх %.1f мс (%d запросов)", ms64(first), q1, ms64(best), q)
	if best > 500*time.Millisecond {
		t.Errorf("«Здоровье» собирается %.0f мс при бюджете 500 мс на этих данных", ms64(best))
	}
}

// Меню собирается на каждом экране — его цена и есть пол любого экрана
// (3 октября 2026 на Pi 40–60 мс). Прибор без порога: с -v видно, сколько
// времени и запросов уходит на меню на данных живого размера.
func TestMenuCostOnLiveSizedData(t *testing.T) {
	s := healthLiveSized(t)
	var best *screenTiming
	for i := 0; i < 4; i++ {
		tm := &screenTiming{start: time.Now(), sql: store.TraceSQL()}
		tm.sql.Keep = true
		if _, err := s.buildScreenForPhone("settings:tools", "", "", 0, false, false, timedBy(tm)); err != nil {
			t.Fatal(err)
		}
		tm.stop()
		if best == nil || tm.menu < best.menu {
			best = tm
		}
	}
	t.Logf("меню: %.2f мс, %d запросов к базе", ms64(best.menu), best.menuSQL)
	counts := map[string]int{}
	for _, q := range best.sql.Queries()[:best.menuSQL] {
		if len([]rune(q)) > 90 {
			q = string([]rune(q)[:90])
		}
		counts[q]++
	}
	for q, n := range counts {
		t.Logf("%3d × %s", n, q)
	}
}

// listMarkup — разметка экрана так, как её получает браузер (без сжатия).
func listMarkup(t *testing.T, s *Server, screen string) string {
	t.Helper()
	r := httptest.NewRequest("GET", "/ui?screen="+url.QueryEscape(screen), nil)
	r.Header.Set("HX-Request", "true")
	r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:8765"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%s: %d", screen, w.Code)
	}
	return w.Body.String()
}

var markupTag = regexp.MustCompile(`<[a-z][a-z0-9]*[ >/]`)

// Тяжёлые списки на данных живого размера (perf-pass 5.3): 30 модулей, 88
// скриптов, 31 задача, 6 областей памяти и 31 потребитель. 3 октября 2026 на
// живой Сёме «Модули» весили 153 КБ (код модулей ехал в список), «Скрипты» —
// 1263 элемента (у каждого пункта панели — меню с вопросом «Удалить?»),
// «Память → Области» — 1709 (в раскрытии каждой области — все потребители).
// Здесь — то, что держит каждую из трёх правок; размеры — в журнал с -v.
func TestHeavyListsStayLight(t *testing.T) {
	s := healthLiveSized(t)
	st := s.eng.Store()
	code := "function run() { return 42; } // ОСОБАЯ-СТРОКА-КОДА"
	_ = s.eng.InstallModule(&engine.Module{Name: "marker", Title: "Маркер", Code: code})
	for i := 0; i < 88; i++ {
		id, _ := st.CreateScript(fmt.Sprintf("Скрипт номер %d · из чата", i), strings.Repeat("// строка кода\n", 40)+"return 1;")
		if i < 31 {
			_ = st.CreateTask(&store.Task{Title: fmt.Sprintf("Задача %d", i), Kind: "script", ScriptID: id, Schedule: "каждые 5 мин", Enabled: i%2 == 0})
		}
	}
	var areas []string
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("area%d", i)
		areas = append(areas, name)
		_ = st.SaveMemoryArea(&store.MemoryArea{Name: name, Title: fmt.Sprintf("Область %d", i)})
	}
	for i := 0; i < 31; i++ {
		c, err := st.MemoryConsumer("mcp", fmt.Sprintf("агент-%d", i%3), fmt.Sprintf("/Users/v/repo/проект-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		_ = st.SetConsumerAreas(c.ID, areas)
	}

	modules := listMarkup(t, s, "modules")
	if strings.Contains(modules, "ОСОБАЯ-СТРОКА-КОДА") {
		t.Error("код модуля едет в список «Модулей», а должен жить на экране модуля")
	}
	if page := listMarkup(t, s, "modules@marker"); !strings.Contains(page, "ОСОБАЯ-СТРОКА-КОДА") {
		t.Error("на экране модуля нет его кода")
	}
	scripts := listMarkup(t, s, "scripts")
	if n := strings.Count(scripts, `"action":"delete_script"`) + strings.Count(scripts, `&#34;delete_script&#34;`); n > 1 {
		t.Errorf("«Удалить скрипт» встречается %d раз: у пунктов панели его быть не должно, только на экране скрипта", n)
	}
	areasPage := listMarkup(t, s, "memory~areas")
	if n := strings.Count(areasPage, "/Users/v/repo/проект-"); n > 31*2 {
		t.Errorf("потребители повторены %d раз: в раскрытии каждой области их быть не должно — кому выдана, говорит матрица", n)
	}
	for name, body := range map[string]string{"modules": modules, "scripts": scripts, "memory~areas": areasPage} {
		t.Logf("%-13s %6.1f КБ, %5d элементов", name, float64(len(body))/1024, len(markupTag.FindAllString(body, -1)))
	}
}
