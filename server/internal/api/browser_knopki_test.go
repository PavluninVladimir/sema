package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Кнопки одного экрана — одного роста: «Выключить режим» в строке была 28
// точек, «Свернуть» в шапке раздела — 32, и рядом это читалось как две
// разные системы (13 сентября 2026).
func TestBrowserButtonsSameHeight(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	for _, screen := range []string{"inbox", "health", "tasks", "settings:servers"} {
		t.Run(screen, func(t *testing.T) {
			s, _ := testServer(t)
			srv := httptest.NewServer(s.Handler())
			defer srv.Close()
			alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
				append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
			defer cancelAlloc()
			ctx, cancel := chromedp.NewContext(alloc)
			defer cancel()
			ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
			defer cancelTime()
			if err := chromedp.Run(ctx,
				chromedp.EmulateViewport(1200, 900),
				chromedp.Navigate(srv.URL+"/#screen="+screen)); err != nil {
				if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
					t.Skipf("Chrome не найден: %v", err)
				}
				t.Fatalf("открыть страницу: %v", err)
			}
			waitScreen(t, ctx, screen)
			// у всех кнопок внутри ленты одна высота: она задана токенами
			var беды []string
			if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
				const беды = [];
				const роста = new Map();
				document.querySelectorAll('#feed button.action').forEach((б) => {
					const h = Math.round(б.getBoundingClientRect().height);
					if (h === 0) return;
					const ключ = б.closest('.row, .shelf-row, .section-header, .table, .row-group') ? 'в списке' : 'сам по себе';
					if (!роста.has(ключ)) роста.set(ключ, new Map());
					const м = роста.get(ключ);
					м.set(h, (м.get(h) || 0) + 1);
				});
				роста.forEach((м, ключ) => {
					if (м.size > 1) беды.push(ключ + ': высоты ' + [...м.keys()].join(', '));
				});
				return беды;
			})()`, &беды)); err != nil {
				t.Fatal(err)
			}
			for _, б := range беды {
				t.Errorf("%s: кнопки разного роста — %s", screen, б)
			}
		})
	}
}
