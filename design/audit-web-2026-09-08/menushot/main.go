package main

import (
	"context"
	"flag"
	"os"
	"time"

	"github.com/chromedp/chromedp"
)

func main() {
	base := flag.String("base", "http://sema.local:8765", "адрес")
	screen := flag.String("screen", "tasks", "экран")
	out := flag.String("out", "меню.png", "куда")
	js := flag.String("js", "", "что выполнить после открытия меню")
	w := flag.Int("w", 390, "ширина")
	h := flag.Int("h", 844, "высота")
	flag.Parse()
	alloc, c1 := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer c1()
	ctx, c2 := chromedp.NewContext(alloc)
	defer c2()
	ctx, c3 := context.WithTimeout(ctx, 60*time.Second)
	defer c3()
	var buf []byte
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(int64(*w), int64(*h)),
		chromedp.Navigate(*base+"/#screen="+*screen),
		chromedp.Sleep(4*time.Second),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.Sleep(600*time.Millisecond),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if *js == "" {
				return nil
			}
			return chromedp.Evaluate(*js, nil).Do(ctx)
		}),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.CaptureScreenshot(&buf),
	)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		panic(err)
	}
}
