package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Профилировщик: по умолчанию его нет (404), включённый отвечает праву
// «всё», а токену «экраны» — отказ с записью в журнале доступа; пока он
// включён, «Здоровье» об этом говорит (perf-pass 1.3).
func TestPprofBehindSettingAndAllScope(t *testing.T) {
	s, st := testServer(t)
	const remote = "203.0.113.7:4000" // не своя сеть: пускает только токен
	all, err := st.AddToken("всё", []string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	screens, err := st.AddToken("экраны", []string{"ui"})
	if err != nil {
		t.Fatal(err)
	}

	if w := doRequest(s, "GET", "/debug/pprof/", "", remote, all); w.Code != http.StatusNotFound {
		t.Fatalf("без настройки ответ %d, ждали 404", w.Code)
	}
	if strings.Contains(healthText(t, s), "Профилировщик включён") {
		t.Error("профилировщик выключен, а «Здоровье» говорит, что включён")
	}

	if err := st.SetSetting(engine.DebugPprofKey, "1"); err != nil {
		t.Fatal(err)
	}
	if w := doRequest(s, "GET", "/debug/pprof/", "", remote, all); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "goroutine") {
		t.Fatalf("с правом «всё» ответ %d: %.200s", w.Code, w.Body.String())
	}
	before, _ := st.AccessLog(100)
	if w := doRequest(s, "GET", "/debug/pprof/heap", "", remote, screens); w.Code == http.StatusOK {
		t.Fatalf("токен «экраны» получил профиль: %d", w.Code)
	}
	after, _ := st.AccessLog(100)
	if len(after) <= len(before) {
		t.Error("отказ токену «экраны» не записан в журнал доступа")
	}
	if !strings.Contains(healthText(t, s), "Профилировщик включён") {
		t.Error("профилировщик включён, а на «Здоровье» об этом ни строки")
	}
}

func healthText(t *testing.T, s *Server) string {
	t.Helper()
	doc, err := s.buildScreen("health", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return dumpScreen(doc)
}
