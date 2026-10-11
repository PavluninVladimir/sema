package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/version"
)

func TestCheckNewVersion(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	updateAllowLocal = true
	t.Cleanup(func() { updateAllowLocal = false })
	if got := e.updatePass(context.Background()); !strings.Contains(got, "не задан") {
		t.Fatalf("без адреса проверка должна молчать: %q", got)
	}
	var seenVersion, seenUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenVersion, seenUA = r.URL.Query().Get("v"), r.UserAgent()
		if len(r.URL.Query()) != 1 {
			t.Errorf("наружу ушло лишнее: %v", r.URL.Query())
		}
		_, _ = w.Write([]byte(`{"version":"v9.9.9","notes":"тест"}`))
	}))
	defer srv.Close()
	if err := e.SetUpdateURL("ftp://x"); err == nil {
		t.Fatal("ftp принят")
	}
	if err := e.SetUpdateURL("http://example.com/latest"); err == nil {
		t.Fatal("http без шифрования принят")
	}
	if err := e.store.SetSetting(updateURLKey, srv.URL+"/latest"); err != nil { // тестовый сервер — http, кладём мимо проверки схемы
		t.Fatal(err)
	}
	got := e.updatePass(context.Background())
	if !strings.Contains(got, "v9.9.9") || seenVersion != version.String() || !strings.HasPrefix(seenUA, "Sema/") {
		t.Fatalf("проверка: %q, ушла версия %q, UA %q", got, seenVersion, seenUA)
	}
	list, _ := st.Notifications(true, 50)
	found := false
	for _, n := range list {
		if strings.HasPrefix(n.Key, "update:v9.9.9") {
			found = true
		}
	}
	if !found {
		t.Fatal("новая версия не пришла карточкой")
	}
	// запрос виден на «Что ушло»
	rows, _ := st.SentList(false, "", "", 20)
	seen := false
	for _, r := range rows {
		if r.Consumer == "core:update" && r.OK {
			seen = true
		}
	}
	if !seen {
		t.Fatal("запрос не записан в «Что ушло»")
	}
	// источник лёг — строка результата, не ошибка и не карточка
	srv.Close()
	if got := e.updatePass(context.Background()); !strings.Contains(got, "не ответил") {
		t.Fatalf("неудача должна быть строкой: %q", got)
	}
}

// Адрес в своей сети и переадресация — отказ: проверка версии не должна
// становиться способом ходить по домашней сети от имени Сёмы.
func TestCheckVersionNotGoesInOwnNet(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusFound)
	}))
	defer srv.Close()
	if err := e.store.SetSetting(updateURLKey, srv.URL); err != nil {
		t.Fatal(err)
	}
	if got := e.updatePass(context.Background()); !strings.Contains(got, "свою сеть") {
		t.Fatalf("адрес в своей сети прошёл: %q", got)
	}
	updateAllowLocal = true
	defer func() { updateAllowLocal = false }()
	if got := e.updatePass(context.Background()); !strings.Contains(got, "переадресац") {
		t.Fatalf("переадресация прошла: %q", got)
	}
}
