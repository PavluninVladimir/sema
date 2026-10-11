package api

import (
	"strings"
	"testing"
)

// Чтение страниц по ссылке правится на экране «Поиск», а не в коде: шесть
// чисел, ноль у ссылок законен («не читать»), лишнее отвергается словами.
func TestReadSettingsEditableOnSearchScreen(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("settings:search", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpWithRare(t, s, "settings:search", doc) // пределы — в «Тонкой настройке»
	for _, field := range []string{"read_links", "read_chars", "page_chars", "page_links", "page_min_chars", "read_ttl_h", "set_read_settings"} {
		if !strings.Contains(dump, field) {
			t.Errorf("на экране «Поиск» нет %q", field)
		}
	}
	if s.eng.ReadLinksCount() != 3 || s.eng.ReadLinksChars() != 12000 || s.eng.WebPageChars() != 30000 || s.eng.WebPageLinks() != 100 {
		t.Fatalf("умолчания: %d %d %d %d", s.eng.ReadLinksCount(), s.eng.ReadLinksChars(), s.eng.WebPageChars(), s.eng.WebPageLinks())
	}
	if s.eng.WebPageMinChars() != 200 || s.eng.ReadLinksTTLHours() != 24 {
		t.Fatalf("умолчания срока и предела: %d %d", s.eng.WebPageMinChars(), s.eng.ReadLinksTTLHours())
	}
	set := func(params map[string]string) error {
		_, err := s.execute(actionRequest{Action: "set_read_settings", Params: params})
		return err
	}
	if err := set(map[string]string{"page_min_chars": "0", "read_ttl_h": "0"}); err != nil {
		t.Fatal(err)
	}
	if s.eng.WebPageMinChars() != 0 || s.eng.ReadLinksTTLHours() != 0 {
		t.Errorf("ноль законен у предела и у срока: %d %d", s.eng.WebPageMinChars(), s.eng.ReadLinksTTLHours())
	}
	if err := set(map[string]string{"read_links": "0", "read_chars": "5000", "page_chars": "80000", "page_links": "0"}); err != nil {
		t.Fatal(err)
	}
	if s.eng.ReadLinksCount() != 0 || s.eng.ReadLinksChars() != 5000 || s.eng.WebPageChars() != 80000 || s.eng.WebPageLinks() != 0 {
		t.Errorf("после сохранения: %d %d %d %d", s.eng.ReadLinksCount(), s.eng.ReadLinksChars(), s.eng.WebPageChars(), s.eng.WebPageLinks())
	}
	for _, bad := range []map[string]string{{"read_links": "-1"}, {"read_links": "много"}, {"read_chars": "10"}, {"page_chars": "999999999"}, {"page_links": "5000"}, {"page_min_chars": "-1"}, {"read_ttl_h": "100000"}} {
		if err := set(bad); err == nil {
			t.Errorf("значение %v принято", bad)
		}
	}
}
