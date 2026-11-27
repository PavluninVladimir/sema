package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGitHubReleaseWebhook(t *testing.T) {
	s, st := testServer(t)
	key := strings.Repeat("a", 32)
	t.Setenv("SEMA_RELEASE_WEBHOOK_SECRET", key)
	t.Setenv("SEMA_RELEASE_REPOSITORY", "example/sema")
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
	post(key, payload, http.StatusNoContent)
	post(key, payload, http.StatusNoContent)
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
