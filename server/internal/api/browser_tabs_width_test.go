package api

// Полоса вкладок на узких экранах: выбранная вкладка видна целиком после
// перехода — на 320, 360 и 390 точках. Последняя из шести подвкладок
// «Почты» оставалась за правым краем: экран менялся, а в полосе не было
// видно, что выбрано.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestBrowserSelectedTabVisibleWhenNarrow(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()

	// последняя подвкладка «Почты» и последняя из пяти постоянных
	screens := []string{"settings:mail~" + mailTabSpam, accessScreen}
	for _, ширина := range []int64{320, 360, 390} {
		for _, screen := range screens {
			ctx, cancel := chromedp.NewContext(alloc)
			ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
			if err := chromedp.Run(ctx,
				chromedp.EmulateViewport(ширина, 760),
				chromedp.Navigate(srv.URL+"/#screen="+screen)); err != nil {
				if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
					t.Skipf("Chrome не найден: %v", err)
				}
				t.Fatalf("открыть страницу: %v", err)
			}
			waitScreen(t, ctx, screen)
			// выбранный сегмент в каждой полосе, что едет вбок, лежит внутри
			// её видимой части и внутри окна
			var беды []string
			if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
				const беды = [];
				document.querySelectorAll('.segments').forEach((ряд) => {
					const в = ряд.querySelector('.segment.selected');
					if (!в) return;
					const р = ряд.getBoundingClientRect(), с = в.getBoundingClientRect();
					const внутри = с.left >= р.left - 1 && с.right <= р.right + 1 && с.right <= window.innerWidth + 1 && с.left >= -1;
					if (!внутри) беды.push((в.textContent || '').trim() + ': ' + Math.round(с.left) + '…' + Math.round(с.right) + ' при полосе ' + Math.round(р.left) + '…' + Math.round(р.right));
				});
				return беды;
			})()`, &беды)); err != nil {
				t.Fatal(err)
			}
			for _, б := range беды {
				t.Errorf("%d px, %s: выбранная вкладка обрезана — %s", ширина, screen, б)
			}
			// тело страницы вбок не едет никогда
			var уезжает bool
			if err := chromedp.Run(ctx, chromedp.Evaluate(
				`document.documentElement.scrollWidth > window.innerWidth + 1`, &уезжает)); err != nil {
				t.Fatal(err)
			}
			if уезжает {
				t.Errorf("%d px, %s: страница прокручивается вбок", ширина, screen)
			}
			cancelTime()
			cancel()
			_ = fmt.Sprint
		}
	}
}
