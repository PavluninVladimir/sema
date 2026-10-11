package api

import (
	"github.com/PavluninVladimir/sema/server/internal/web"

	"os"
	"strings"
	"testing"
)

// Сборка страницы в файл: нужна, чтобы проверить вёрстку в настоящем
// браузере на узких экранах, не заводя сессию. Пишет файл только когда
// попросили переменной (латиницей: кириллица в именах переменных
// окружения через оболочку не проходит — на этом уже спотыкались), иначе обычный прогон тестов мусорит.
func TestDumpPageToFile(t *testing.T) {
	куда := os.Getenv("SEMA_PAGE_OUT")
	if куда == "" {
		t.Skip("нужен путь в SEMA_PAGE_OUT")
	}
	s, _ := testServer(t)
	screen := os.Getenv("SEMA_PAGE_SCREEN")
	if screen == "" {
		screen = "inbox"
	}
	doc, err := s.buildScreen(screen, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	html, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatal(err)
	}
	стиль, err := os.ReadFile("../web/static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	страница := `<!DOCTYPE html><html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>проба вёрстки</title><style>` + string(стиль) + `</style></head><body>
<div id="window">` + string(html) + `</div></body></html>`
	if err := os.WriteFile(куда, []byte(страница), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("страница экрана %q собрана: %s (%d КБ)", screen, куда, len(страница)/1024)
	if !strings.Contains(страница, "окно") {
		t.Fatal("страница вышла пустой")
	}
}
