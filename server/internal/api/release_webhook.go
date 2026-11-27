package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const releaseWebhookPath = "/api/webhooks/github-release"

var githubBuildVersion = regexp.MustCompile(`^build-[0-9a-f]{40}$`)

type githubReleaseNotice struct {
	Repository string `json:"repository"`
	Version    string `json:"version"`
	Sent       int64  `json:"sent"`
}

func (s *Server) handleGitHubRelease(w http.ResponseWriter, r *http.Request) {
	key := os.Getenv("SEMA_RELEASE_WEBHOOK_SECRET")
	signature, ok := strings.CutPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
	if len(key) < 32 || !ok || len(signature) != 64 {
		http.NotFound(w, r)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		http.Error(w, "неверное извещение о выпуске", http.StatusBadRequest)
		return
	}
	provided, err := hex.DecodeString(signature)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(raw)
	if err != nil || !hmac.Equal(provided, mac.Sum(nil)) {
		http.NotFound(w, r)
		return
	}
	repository := os.Getenv("SEMA_RELEASE_REPOSITORY")
	if repository == "" {
		repository = "PavluninVladimir/sema"
	}
	var notice githubReleaseNotice
	if json.Unmarshal(raw, &notice) != nil || !strings.EqualFold(repository, notice.Repository) ||
		!githubBuildVersion.MatchString(notice.Version) || time.Since(time.Unix(notice.Sent, 0)).Abs() > 10*time.Minute {
		http.Error(w, "неверное извещение о выпуске", http.StatusBadRequest)
		return
	}
	if _, err := s.eng.Store().PutNotification(&store.Notification{
		Key: "github-release:" + notice.Version, Source: "core", Level: "ok",
		Title: "На GitHub опубликована сборка Сёмы", Summary: notice.Version, State: store.NotifyOpen,
	}); err != nil {
		http.Error(w, "не удалось сохранить извещение", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
