// семашот — снимки экранов живой Сёмы и макетов для аудита: картинка плюс
// «шкала» страницы (размеры шрифтов, цвета, радиусы, отступы) в JSON.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// metrics — JS, собирающий шкалу страницы: что за шрифты, цвета, радиусы
// и отступы встречаются, и сколько раз. По этому видно систему, а не
// отдельный элемент.
const metrics = `(() => {
  const seen = {fonts:{}, colors:{}, bg:{}, radius:{}, pad:{}, gaps:{}, icons:0, svg:0, imgs:0};
  const els = document.querySelectorAll('body *');
  let n = 0;
  for (const el of els) {
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden') continue;
    const r = el.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) continue;
    n++;
    const text = (el.childNodes.length && [...el.childNodes].some(c => c.nodeType === 3 && c.textContent.trim())) ? el.textContent.trim().slice(0, 40) : '';
    if (text) {
      const k = cs.fontSize + '/' + cs.fontWeight + (cs.textTransform === 'uppercase' ? '/UPPER' : '') + (cs.letterSpacing !== 'normal' ? '/ls' + cs.letterSpacing : '');
      seen.fonts[k] = seen.fonts[k] || {n:0, sample:[]};
      seen.fonts[k].n++;
      if (seen.fonts[k].sample.length < 3) seen.fonts[k].sample.push(text);
      const c = cs.color; seen.colors[c] = (seen.colors[c]||0)+1;
    }
    const bg = cs.backgroundColor;
    if (bg && bg !== 'rgba(0, 0, 0, 0)') seen.bg[bg] = (seen.bg[bg]||0)+1;
    const br = cs.borderTopLeftRadius;
    if (br && br !== '0px') seen.radius[br] = (seen.radius[br]||0)+1;
    const p = [cs.paddingTop, cs.paddingRight, cs.paddingBottom, cs.paddingLeft].join(' ');
    if (p !== '0px 0px 0px 0px' && r.width > 40) seen.pad[p] = (seen.pad[p]||0)+1;
    if (cs.gap && cs.gap !== 'normal' && cs.display.includes('flex')) seen.gaps[cs.gap] = (seen.gaps[cs.gap]||0)+1;
    if (el.tagName === 'svg' || el.tagName === 'SVG') seen.svg++;
    if (el.tagName === 'IMG') seen.imgs++;
  }
  const top = (o, k) => Object.entries(o).sort((a,b)=>(b[1].n||b[1])-(a[1].n||a[1])).slice(0,k);
  return {elements:n, fonts: top(seen.fonts, 14), colors: top(seen.colors, 12), bg: top(seen.bg, 10),
          radius: top(seen.radius, 8), pad: top(seen.pad, 12), gaps: top(seen.gaps, 8), svg: seen.svg, imgs: seen.imgs,
          width: document.documentElement.scrollWidth, height: document.documentElement.scrollHeight,
          title: (document.querySelector('h1')||{}).textContent || ''};
})()`

func main() {
	base := flag.String("base", "http://sema.local:8765", "адрес Сёмы")
	out := flag.String("out", "снимки", "куда класть")
	screens := flag.String("screens", "", "экраны через запятую")
	mockups := flag.String("mockups", "", "файлы макетов через запятую (полные пути)")
	w := flag.Int("w", 1200, "ширина")
	dark := flag.Bool("dark", false, "тёмная тема")
	flag.Parse()
	_ = os.MkdirAll(*out, 0o755)

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"),
			chromedp.Flag("ignore-certificate-errors", true))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancel2 := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel2()

	if err := chromedp.Run(ctx, emulation.SetDeviceMetricsOverride(int64(*w), 780, 1, *w < 720)); err != nil {
		fail(err)
	}
	if *dark {
		if err := chromedp.Run(ctx, emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: "dark"}})); err != nil {
			fail(err)
		}
	}
	if *screens != "" {
		// оболочка грузится один раз, дальше только хэш
		if err := chromedp.Run(ctx, chromedp.Navigate(*base+"/#screen=inbox"), waitScreen()); err != nil {
			fail(fmt.Errorf("оболочка: %w", err))
		}
		for _, sc := range strings.Split(*screens, ",") {
			sc = strings.TrimSpace(sc)
			if sc == "" {
				continue
			}
			name := "live-" + strings.NewReplacer(":", "_", "@", "_", "~", "_", "/", "_").Replace(sc)
			err := chromedp.Run(ctx,
				chromedp.Evaluate(`location.hash = '#screen=`+sc+`'`, nil),
				chromedp.Sleep(700*time.Millisecond),
				waitScreen(),
				chromedp.Sleep(1200*time.Millisecond),
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", sc, err)
				continue
			}
			shoot(ctx, filepath.Join(*out, name))
		}
	}
	for _, m := range strings.Split(*mockups, ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		name := "mock-" + strings.TrimSuffix(filepath.Base(m), ".dc.html")
		if err := chromedp.Run(ctx, chromedp.Navigate("file://"+m), chromedp.Sleep(800*time.Millisecond)); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", m, err)
			continue
		}
		shoot(ctx, filepath.Join(*out, name))
	}
}

func waitScreen() chromedp.Action {
	var ok bool
	return chromedp.Poll(`!!document.querySelector('#окно') && document.querySelector('#окно').children.length > 0 && !document.querySelector('.htmx-request')`,
		&ok, chromedp.WithPollingTimeout(20*time.Second))
}

func shoot(ctx context.Context, path string) {
	var png []byte
	var m map[string]any
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(metrics, &m),
		chromedp.FullScreenshot(&png, 80),
	); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		return
	}
	_ = os.WriteFile(path+".png", png, 0o644)
	raw, _ := json.MarshalIndent(m, "", " ")
	_ = os.WriteFile(path+".json", raw, 0o644)
	fmt.Printf("%s: %v×%v, элементов %v\n", filepath.Base(path), m["width"], m["height"], m["elements"])
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
