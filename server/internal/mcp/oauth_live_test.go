package mcp

import (
	"context"
	"os"
	"testing"
	"time"
)

// Проверка обнаружения на настоящем сервере: только чтение .well-known, ни
// регистрации, ни входа. Включается явно, в обычном прогоне пропускается:
//
//	SEMA_LIVE_MCP=https://mcp.planner.example/mcp go test ./internal/mcp -run LiveDiscovery -v
func TestLiveDiscovery(t *testing.T) {
	url := os.Getenv("SEMA_LIVE_MCP")
	if url == "" {
		t.Skip("SEMA_LIVE_MCP не задан")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, err := Net{}.Discover(ctx, url, nil)
	if err != nil {
		t.Fatalf("обнаружение: %v", err)
	}
	t.Logf("resource: %s", d.Resource)
	t.Logf("метаданные ресурса: %s (запасной путь: %v)", d.MetadataURL, d.Fallback)
	t.Logf("страница входа: %s", d.Server.AuthorizationEndpoint)
	t.Logf("выдача токенов: %s", d.Server.TokenEndpoint)
	t.Logf("регистрация: %s", d.Server.RegistrationEndpoint)
	t.Logf("отзыв: %s", d.Server.RevocationEndpoint)
	t.Logf("права (%d): %v", len(d.Scopes), d.Scopes)
	if d.Server.AuthorizationEndpoint == "" || d.Server.TokenEndpoint == "" {
		t.Error("нет страницы входа или выдачи токенов")
	}
}
