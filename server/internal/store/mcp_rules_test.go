package store

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Правила серверов MCP переживают перечитывание и сохранение строки сервера:
// проверка, читавшая строку до решения человека, не должна вернуть старое.
func TestMCPRulesSurviveServerSave(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveMCPServer(&MCPServer{Name: "a", URL: "http://x/mcp", Tools: []string{"t1", "t2"}}); err != nil {
		t.Fatal(err)
	}
	old, _ := s.MCPServer("a")
	if !old.RulesSet || len(old.Rules) != 0 || old.RuleDefault != RuleDeny {
		t.Fatalf("у нового сервера правила заведены и пусты, а по умолчанию «Запретить»: %+v", old)
	}
	// человек решил, пока проверка держала старую строку
	if err := s.SetMCPRules("a", map[string]string{"t1": RuleAllow, "t2": RuleAsk}, RuleAsk); err != nil {
		t.Fatal(err)
	}
	old.Tools = []string{"t1", "t2", "t3"}
	if err := s.SaveMCPServer(old); err != nil {
		t.Fatal(err)
	}
	got, _ := s.MCPServer("a")
	if got.Rules["t1"] != RuleAllow || got.Rules["t2"] != RuleAsk || got.RuleDefault != RuleAsk {
		t.Fatalf("сохранение строки затёрло решение человека: %+v", got)
	}
	if len(got.Tools) != 3 {
		t.Fatalf("сохранение строки потеряло инструменты: %v", got.Tools)
	}
	// ответ не из трёх не записывается вовсе
	if err := s.SetMCPRules("a", map[string]string{"t1": "разрешено"}, RuleDeny); err == nil {
		t.Error("ответ не из трёх принят")
	}
	if err := s.SetMCPRules("нет-такого", nil, RuleDeny); err == nil {
		t.Error("правила записаны серверу, которого нет")
	}
}

// Строка, принесённая прежними кругами (копия, снятая до правил), ещё не
// переехала: у неё есть образцы, а правил нет.
func TestLegacyRowIsNotMigrated(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveMCPServer(&MCPServer{Name: "старый", URL: "http://x/mcp", LegacyRead: []string{"*_list"}}); err != nil {
		t.Fatal(err)
	}
	m, _ := s.MCPServer("старый")
	if m.RulesSet {
		t.Fatal("строка с прежними кругами принята за переехавшую")
	}
	if len(m.LegacyRead) != 1 {
		t.Fatalf("прежние круги не сохранились для переезда: %v", m.LegacyRead)
	}
}
