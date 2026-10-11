package store

import "testing"

// Отбор журнала вызовов и исход строки говорят одно и то же: чип «Ошибки»
// приносит ровно то, что строка называет ошибкой, а «Выполнены» — только
// дошедшее до сервера. Старые слова кругов (read, confirm, never) читаются
// так же, как нынешние.
func TestMCPLogFiltersAgreeWithUseState(t *testing.T) {
	s := openTestStore(t)
	uses := []MCPUse{
		{Server: "a", Tool: "ok_allow", Circle: "allow", OK: true, Result: "готово"},
		{Server: "a", Tool: "ok_legacy", Circle: "read", OK: true, Result: "готово"},
		{Server: "a", Tool: "waits", Circle: "ask", Result: MCPWaitingResult},
		{Server: "a", Tool: "approved", Circle: "ask", OK: true, Result: "готово"},
		{Server: "a", Tool: "rejected", Circle: "ask", Result: MCPRejectedResult},
		{Server: "a", Tool: "denied", Circle: "deny", Result: "правило"},
		{Server: "a", Tool: "denied_legacy", Circle: "never", Result: "правило"},
		{Server: "a", Tool: "broken", Circle: "allow", Result: "сервер ответил 500"},
		{Server: "a", Tool: "broken_after_ask", Circle: "ask", Result: "сервер ответил 500"},
		{Server: "b", Tool: "other_server", Circle: "allow", OK: true, Result: "готово"},
	}
	for _, u := range uses {
		s.LogMCP(u)
	}
	want := map[string]map[string]bool{
		MCPLogAll:    nil,
		MCPLogDone:   {"ok_allow": true, "ok_legacy": true, "approved": true},
		MCPLogAsked:  {"waits": true, "approved": true, "rejected": true, "broken_after_ask": true},
		MCPLogDenied: {"denied": true, "denied_legacy": true, "rejected": true},
		MCPLogError:  {"broken": true, "broken_after_ask": true},
	}
	for filter, tools := range want {
		rows, err := s.MCPLogFiltered("a", filter, 100)
		if err != nil {
			t.Fatal(err)
		}
		if filter == MCPLogAll {
			if len(rows) != 9 {
				t.Errorf("все вызовы сервера a: %d, а их девять (чужой сервер не считается)", len(rows))
			}
			continue
		}
		got := map[string]bool{}
		for _, u := range rows {
			got[u.Tool] = true
		}
		if len(got) != len(tools) {
			t.Errorf("отбор %q: %v, ждали %v", filter, got, tools)
		}
		for tool := range tools {
			if !got[tool] {
				t.Errorf("отбор %q потерял %q", filter, tool)
			}
		}
		if n := s.CountMCPLog("a", filter); n != len(tools) {
			t.Errorf("число у чипа %q: %d, а строк %d", filter, n, len(tools))
		}
	}
	// без имени сервера — журнал всех
	if n := s.CountMCPLog("", MCPLogAll); n != 10 {
		t.Errorf("журнал всех серверов: %d", n)
	}
	// исход строки
	for tool, state := range map[string]string{"ok_allow": MCPUseDone, "waits": MCPUseWaiting, "rejected": MCPUseRejected,
		"denied": MCPUseDenied, "denied_legacy": MCPUseDenied, "broken": MCPUseError} {
		rows, _ := s.MCPLogFiltered("a", MCPLogAll, 100)
		for _, u := range rows {
			if u.Tool == tool && u.State() != state {
				t.Errorf("%s: исход %q, а ждали %q", tool, u.State(), state)
			}
		}
	}
	// предел и порядок: свежие первыми
	rows, _ := s.MCPLogFiltered("a", MCPLogAll, 2)
	if len(rows) != 2 || rows[0].Tool != "broken_after_ask" {
		t.Errorf("предел и порядок: %+v", rows)
	}
}
