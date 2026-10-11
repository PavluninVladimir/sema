package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// Трекеры задач сняты 30 сентября 2026: на «Интеграциях» и в поиске по
// настройкам их больше нет, а старая закладка «~trackers» открывает экран,
// а не ошибку.
func TestIntegrationsHaveNoTrackers(t *testing.T) {
	s, _ := testServer(t)
	for _, screen := range []string{mcpScreen, mcpScreen + "~trackers", "settings"} {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("экран %s: %v", screen, err)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		// адрес, который человек сам открыл, эхом лежит в документе: это не
		// след возможности, а его собственный запрос
		low := strings.ToLower(strings.ReplaceAll(string(raw), screen, ""))
		for _, bad := range []string{"трекер", "tracker"} {
			if strings.Contains(low, bad) {
				i := strings.Index(low, bad)
				t.Errorf("экран %s: осталось «%s» — …%s…", screen, bad, string([]rune(low[max(0, i-40):min(len(low), i+60)])))
			}
		}
	}
	// у экрана одна вкладка, а значит полосы вкладок нет
	doc, err := s.buildScreen(mcpScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Subtabs) != 0 {
		t.Errorf("у «Интеграций» осталась полоса внутренних вкладок: %d", len(doc.Subtabs))
	}
	if strings.Contains(strings.ToLower(settingsWords()), "трекер") {
		t.Error("поиск по настройкам всё ещё знает слово «трекер»")
	}
}

// Действий снятых трекеров сервер больше не исполняет: старая страница в
// открытой вкладке получает отказ, а не молча меняет то, чего нет.
func TestTrackerActionsAreGone(t *testing.T) {
	s, _ := testServer(t)
	for _, a := range []string{"add_tracker", "toggle_tracker", "set_tracker_rule", "poll_tracker", "delete_tracker"} {
		if _, err := s.execute(actionRequest{Action: a, Target: "Planner"}); err == nil ||
			!strings.Contains(err.Error(), "unknown action") {
			t.Errorf("действие %s: ждали «unknown action», получили %v", a, err)
		}
	}
}
