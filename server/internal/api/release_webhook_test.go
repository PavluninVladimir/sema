package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/auth"
)

func TestGitHubReleaseWebhook(t *testing.T) {
	s, st := testServer(t)
	key := strings.Repeat("a", 32)
	t.Setenv("SEMA_RELEASE_WEBHOOK_SECRET", key)
	t.Setenv("SEMA_RELEASE_REPOSITORY", "example/sema")
	signalDir := filepath.Join(s.eng.DataDir(), releaseSignalDir)
	if err := os.Mkdir(signalDir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(signalDir, "request")
	handler := s.Handler()
	payload := githubReleaseNotice{"example/sema", "build-" + strings.Repeat("b", 40), time.Now().Unix()}

	post := func(key string, notice githubReleaseNotice, want int) {
		t.Helper()
		raw, err := json.Marshal(notice)
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, []byte(key))
		_, _ = mac.Write(raw)
		req := httptest.NewRequest(http.MethodPost, "https://sema.example.org"+releaseWebhookPath, strings.NewReader(string(raw)))
		req.RemoteAddr = снаружиАдрес
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Errorf("HTTP %d вместо %d: %s", w.Code, want, w.Body.String())
		}
	}
	post("неверный ключ", payload, http.StatusNotFound)
	stale := payload
	stale.Sent -= 1200
	post(key, stale, http.StatusBadRequest)
	stale = payload
	stale.Repository = "another/sema"
	post(key, stale, http.StatusBadRequest)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("невалидный запрос оставил сигнал: %v", err)
	}
	post(key, payload, http.StatusNoContent)
	post(key, payload, http.StatusNoContent)
	if file, err := os.Stat(marker); err != nil || file.Size() != 0 {
		t.Fatalf("нет пустого файла сигнала: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(releaseImmediate.Key, "false"); err != nil {
		t.Fatal(err)
	}
	post(key, payload, http.StatusNoContent)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("выключенный webhook оставил сигнал: %v", err)
	}
	notices, err := st.Notifications(true, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, notice := range notices {
		if notice.Key == "github-release:"+payload.Version {
			count++
		}
	}
	if count != 1 {
		t.Errorf("ожидалось одно извещение, получено %d", count)
	}
}

func TestGitHubReleaseSettings(t *testing.T) {
	s, st := testServer(t)
	key := strings.Repeat("x", 32)
	t.Setenv("SEMA_RELEASE_WEBHOOK_SECRET", key)
	t.Setenv("SEMA_RELEASE_REPOSITORY", "example/sema")
	if err := st.SetSetting(auth.SettingOrigin, "https://sema.example.org"); err != nil {
		t.Fatal(err)
	}
	screen := entityAddr(watchScreen, "", workTabUpdates)
	path := "/api/ui?screen=" + url.QueryEscape(screen)
	local := "127.0.0.1:5000"
	get := doRequest(s, http.MethodGet, path, "", local, "")
	for _, want := range []string{"Webhook GitHub", "example/sema", "https://sema.example.org" + releaseWebhookPath,
		"Ожидает настройки службы", "Проверять обновление сразу по webhook"} {
		if !strings.Contains(get.Body.String(), want) {
			t.Errorf("на экране нет %q", want)
		}
	}
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), key) {
		t.Fatalf("неверный доступ к настройкам webhook: HTTP %d", get.Code)
	}
	guest, err := st.AddToken("читатель", []string{"ui", "actions"})
	if err != nil {
		t.Fatal(err)
	}
	const change = `{"action":"set_release_webhook","screen":"settings:watch~updates","params":{"immediate":"false"}}`
	if response := doRequest(s, http.MethodPost, "/api/action", change, снаружиАдрес, guest); response.Code != http.StatusForbidden {
		t.Fatalf("ограниченный токен изменил настройку: HTTP %d", response.Code)
	}
	if response := doRequest(s, http.MethodGet, path, "", снаружиАдрес, guest); response.Code != http.StatusForbidden {
		t.Fatalf("ограниченный токен прочитал настройку: HTTP %d", response.Code)
	}
	if response := doRequest(s, http.MethodPost, "/api/action", change, local, ""); response.Code != http.StatusOK || st.SettingFlag(releaseImmediate) {
		t.Fatalf("владелец не выключил немедленную проверку: HTTP %d", response.Code)
	}
}
