package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Поиск страниц (SearXNG) правится на экране «Поиск»: адрес и три предела,
// значения из коробки названы, лишнее отвергается словами, а «Проверить»
// говорит, что нашлось и какие движки ответили.
func TestWebFindSettingsOnSearchScreen(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen("settings:search", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpWithRare(t, s, "settings:search", doc) // пределы — в «Тонкой настройке»
	for _, want := range []string{"find_url", "find_per_day", "find_gap_ms", "find_cache_min", "set_web_find", "check_web_find", "Поиск страниц (SearXNG)"} {
		if !strings.Contains(dump, want) {
			t.Errorf("на экране «Поиск» нет %q", want)
		}
	}
	if s.eng.WebFindURL() != "" || s.eng.WebFindPerDay() != 100 || s.eng.WebFindGapMS() != 3000 || s.eng.WebFindCacheMin() != 180 {
		t.Fatalf("умолчания: %q %d %d %d", s.eng.WebFindURL(), s.eng.WebFindPerDay(), s.eng.WebFindGapMS(), s.eng.WebFindCacheMin())
	}
	set := func(params map[string]string) error {
		_, err := s.execute(actionRequest{Action: "set_web_find", Params: params})
		return err
	}
	if err := set(map[string]string{"find_url": " http://searxng:8080/search/ ", "find_per_day": "50", "find_gap_ms": "0", "find_cache_min": "0"}); err != nil {
		t.Fatal(err)
	}
	if s.eng.WebFindURL() != "http://searxng:8080" || s.eng.WebFindPerDay() != 50 || s.eng.WebFindGapMS() != 0 || s.eng.WebFindCacheMin() != 0 || !s.eng.WebFindReady() {
		t.Errorf("после сохранения: %q %d %d %d", s.eng.WebFindURL(), s.eng.WebFindPerDay(), s.eng.WebFindGapMS(), s.eng.WebFindCacheMin())
	}
	// пустой адрес выключает поиск страниц
	if err := set(map[string]string{"find_url": ""}); err != nil {
		t.Fatal(err)
	}
	if s.eng.WebFindReady() {
		t.Error("пустой адрес не выключил поиск страниц")
	}
	// плохое отвергается целиком: половина формы не сохраняется
	if err := set(map[string]string{"find_url": "http://searxng:8080"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]string{
		{"find_url": "ftp://searxng"}, {"find_url": "searxng:8080"}, {"find_url": "http://user:pw@searxng:8080"},
		{"find_url": "http://ok:8080", "find_per_day": "0"}, {"find_per_day": "много"}, {"find_gap_ms": "-5"}, {"find_gap_ms": "999999"}, {"find_cache_min": "100000"},
	} {
		if err := set(bad); err == nil {
			t.Errorf("значение %v принято", bad)
		}
	}
	if s.eng.WebFindURL() != "http://searxng:8080" || s.eng.WebFindPerDay() != 50 {
		t.Errorf("отвергнутая форма что-то записала: %q %d", s.eng.WebFindURL(), s.eng.WebFindPerDay())
	}
}

// «Проверить» шлёт пробный запрос по адресу из формы и кладёт результат
// строкой на экран; настройки при этом не меняются.
func TestWebFindCheckButton(t *testing.T) {
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[
			{"url":"https://a.example/","title":"А","content":"а","engine":"yandex","engines":["yandex"]},
			{"url":"https://b.example/","title":"Б","content":"б","engine":"bing","engines":["bing"]}],
			"unresponsive_engines":[["google","CAPTCHA"]]}`))
	}))
	defer searx.Close()
	s, _ := testServer(t)
	if err := s.eng.Store().SetSetting("web.find_gap_ms", "0"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute(actionRequest{Action: "check_web_find", Params: map[string]string{"find_url": searx.URL}}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("settings:search", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	dump := dumpScreen(doc)
	for _, want := range []string{"Проверка:", "нашлось 2", "yandex, bing", "google (CAPTCHA)"} {
		if !strings.Contains(dump, want) {
			t.Errorf("в результате проверки нет %q", want)
		}
	}
	if s.eng.WebFindURL() != "" {
		t.Error("проверка сохранила адрес")
	}
	// не получилось — сказано словами, а не молчанием
	if _, err := s.execute(actionRequest{Action: "check_web_find", Params: map[string]string{"find_url": "ftp://x"}}); err != nil {
		t.Fatal(err)
	}
	doc, _ = s.buildScreen("settings:search", "", "", 0)
	if dump = dumpScreen(doc); !strings.Contains(dump, "не вышло") || !strings.Contains(dump, "http://searxng:8080") {
		t.Errorf("отказ проверки не объяснён: %s", dump)
	}
}
