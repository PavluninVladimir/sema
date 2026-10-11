package api

import (
	"context"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// Холодный старт на телефоне (perf-pass 6.5): пустой кэш, сеть как Wi-Fi
// до Pi за повторителями (100 мс задержки, 2 Мбит/с), окно 390. Прибор, а
// не сторож: запускается с SEMA_MEASURE=1, медиана из трёх — в журнал.
func TestBrowserColdStartMeasure(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" || os.Getenv("SEMA_MEASURE") == "" {
		t.Skip("замер: SEMA_BROWSER=1 SEMA_MEASURE=1 go test ./internal/api -run ColdStart -v")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	var times []time.Duration
	var bytes []float64
	for i := 0; i < 3; i++ {
		alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
			append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
		ctx, cancel := chromedp.NewContext(alloc)
		ctx, cancelTime := context.WithTimeout(ctx, 60*time.Second)
		var received float64
		chromedp.ListenTarget(ctx, func(ev any) {
			if e, ok := ev.(*network.EventLoadingFinished); ok {
				received += e.EncodedDataLength
			}
		})
		start := time.Now()
		err := chromedp.Run(ctx,
			network.Enable(),
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, err := network.EmulateNetworkConditionsByRule([]*network.Conditions{{Latency: 100,
					DownloadThroughput: 2 * 1024 * 1024 / 8, UploadThroughput: 1024 * 1024 / 8}}).Do(ctx)
				return err
			}),
			chromedp.EmulateViewport(390, 844),
			chromedp.Navigate(srv.URL+"/#screen=files"),
			chromedp.Poll(`!!document.getElementById('screen') && !document.getElementById('splash')`, nil,
				chromedp.WithPollingTimeout(40*time.Second)))
		d := time.Since(start)
		cancelTime()
		cancel()
		cancelAlloc()
		if err != nil {
			if strings.Contains(err.Error(), "executable") {
				t.Skipf("Chrome не найден: %v", err)
			}
			t.Fatal(err)
		}
		times = append(times, d)
		bytes = append(bytes, received/1024)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	t.Logf("холодный старт (телефон 390, 100 мс, 2 Мбит/с): медиана %v, всё %v; по сети ≈ %.0f КБ", times[1].Round(time.Millisecond), times, bytes[1])
}
