package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Узел графа памяти — переход на свой экран настоящим жестом: нажатие
// мышью и Enter с клавиатуры. 5 октября 2026 граф «не работал»: узлы были
// кружками без перехода.
func TestBrowserGraphNodeOpensItsScreen(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser")
	}
	s, st := testServer(t)
	центр, err := st.EnsureMemoryNode("сущность", "Сёма", "код")
	if err != nil {
		t.Fatal(err)
	}
	сосед, err := st.EnsureMemoryNode("сущность", "Raspberry Pi", "код")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddMemoryEdge(центр, сосед, "живёт на", 0); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTime()
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1200, 900), chromedp.Navigate(srv.URL+"/#screen=memory:graph")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatal(err)
	}
	waitScreen(t, ctx, "memory:graph")

	// мышью: нажатие на кружок соседа
	target := "memory:node:" + strconv.FormatInt(сосед, 10)
	node := `.graph .node[data-goto="` + target + `"] circle`
	if err := chromedp.Run(ctx, chromedp.Click(node, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать узел: %v", err)
	}
	waitScreen(t, ctx, target)

	// с клавиатуры: фокус на узле центра и Enter
	back := "memory:node:" + strconv.FormatInt(центр, 10)
	if err := chromedp.Run(ctx,
		chromedp.Focus(`.graph .node[data-goto="`+back+`"]`, chromedp.ByQuery),
		chromedp.KeyEvent("\r")); err != nil {
		t.Fatalf("Enter на узле: %v", err)
	}
	waitScreen(t, ctx, back)
}
