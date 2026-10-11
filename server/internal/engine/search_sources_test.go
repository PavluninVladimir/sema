package engine

import "testing"

// Источники поиска доезжают до модуля с заголовком и датой: без даты
// модуль канала не мог отобрать новости «за вчера» и отбрасывал всё.
func TestSourcesSearchWithDate(t *testing.T) {
	answer := "Нашлось: GPT-Live-1 в API.\n\nИсточники:\n" +
		"- Introducing GPT-Live-1 in the API · 2026-09-16 — https://community.openai.com/t/gpt-live-1/1\n" +
		"- Claude Cowork and chat are now one Claude — https://claude.com/blog/cowork.\n" +
		"- https://github.com/microsoft/foundry-dev-tools/blob/main/WHATS_NEW.md\n" +
		"- Introducing GPT-Live-1 in the API · 2026-09-16 — https://community.openai.com/t/gpt-live-1/1\n"
	got := searchSources(answer)
	if len(got) != 3 {
		t.Fatalf("источников не три: %+v", got)
	}
	if got[0].Title != "Introducing GPT-Live-1 in the API" || got[0].Date != "2026-09-16" || got[0].URL != "https://community.openai.com/t/gpt-live-1/1" {
		t.Errorf("первый источник разобран не так: %+v", got[0])
	}
	if got[1].Date != "" || got[1].URL != "https://claude.com/blog/cowork" || got[1].Title == "" {
		t.Errorf("источник без даты разобран не так: %+v", got[1])
	}
	if got[2].Title != "" || got[2].URL == "" {
		t.Errorf("голый адрес разобран не так: %+v", got[2])
	}
	if searchSources("ответ без списка https://x.ru") != nil {
		t.Error("источники нашлись там, где списка нет")
	}
}
