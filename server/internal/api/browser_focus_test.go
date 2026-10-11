package api

// Фокус после переходов и клавиатура без мыши.
//
// P2.2: после подмены экрана фокус предсказуем — выбрали раздел в колонке
// с клавиатуры, остались на выбранном разделе новой колонки; перешли по
// адресу — фокус на заголовке, и скринридер называет, куда попали.
// P2.3: (шторки разделов больше нет — 1 октября 2026 колонка разделов
// стала одной на все настройки, а на телефоне корнем экрана) поле поиска
// в колонке не теряет фокус, пока человек печатает.
// P2.4: масштаб 200 % — кнопки и значения не наезжают на подписи.

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

const keyEnter = "\r"

func openInChrome(t *testing.T, ширина, высота int64, screen string) (context.Context, context.CancelFunc, *httptest.Server) {
	t.Helper()
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	ctx, cancel := chromedp.NewContext(alloc)
	ctx, cancelTime := context.WithTimeout(ctx, 120*time.Second)
	отмена := func() { cancelTime(); cancel(); cancelAlloc(); srv.Close() }
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(ширина, высота),
		chromedp.Navigate(srv.URL+"/#screen="+screen)); err != nil {
		отмена()
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	waitScreen(t, ctx, screen)
	return ctx, отмена, srv
}

// activeElement — что сейчас в фокусе: тег, класс, текст.
func activeElement(t *testing.T, ctx context.Context) string {
	t.Helper()
	var о string
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(() => { const э = document.activeElement; if (!э) return 'нет'; return э.tagName.toLowerCase() + '.' + (э.className || '').toString().split(' ').filter(Boolean).slice(0, 2).join('.') + ' «' + (э.textContent || '').trim().slice(0, 30) + '»'; })()`, &о)); err != nil {
		t.Fatal(err)
	}
	return о
}

func TestBrowserFocusAfterNavigation(t *testing.T) {
	ctx, отмена, srv := openInChrome(t, 1100, 820, "settings")
	defer отмена()

	// раздел «Модели и роли» — с клавиатуры: фокус на пункт колонки, Enter
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(() => { const item = Array.from(document.querySelectorAll('#panel .item')).find((el) => el.textContent.includes('Модели и роли')); item.focus(); return true; })()`, nil),
		chromedp.KeyEvent(keyEnter)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, modelsScreen)
	var onSection bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const el = document.activeElement; return !!el && el.matches('#panel .item.selected') && el.textContent.includes('Модели и роли'); })()`,
		&onSection, chromedp.WithPollingTimeout(5*time.Second))); err != nil || !onSection {
		t.Errorf("после Enter на разделе фокус не на выбранном разделе новой колонки, а на %s", activeElement(t, ctx))
	}

	// переход по адресу — фокус на заголовке
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/#screen=tasks")); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, "tasks")
	var наЗаголовке bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const э = document.activeElement; return !!э && э.tagName === 'H1'; })()`,
		&наЗаголовке, chromedp.WithPollingTimeout(5*time.Second))); err != nil || !наЗаголовке {
		t.Errorf("после перехода по адресу фокус не на заголовке, а на %s", activeElement(t, ctx))
	}
}

// Поле поиска стоит в колонке разделов, и каждая буква перечитывает окно
// целиком вместе с колонкой. Фокус при этом остаётся в поле — иначе набор
// обрывался бы на первой букве, — а не уходит на выбранный раздел, как у
// прочих нажатий внутри колонки.
func TestBrowserColumnSearchKeepsFocus(t *testing.T) {
	ctx, cancel, _ := openInChrome(t, 1100, 820, "settings")
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Click(`#panel #search`, chromedp.ByQuery),
		chromedp.SendKeys(`#panel #search`, "пуш", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	// запрос дошёл до сервера и вернулся в поле: «Нашлось» стоит в Обзоре
	var found bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const field = document.getElementById('search'); return !!field && field.value === 'пуш' && document.getElementById('feed').textContent.includes('Нашлось'); })()`,
		&found, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !found {
		t.Fatalf("поиск не отработал: %v", err)
	}
	var inField bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`document.activeElement === document.getElementById('search') && !!document.activeElement.closest('#panel')`,
		&inField, chromedp.WithPollingTimeout(5*time.Second))); err != nil || !inField {
		t.Errorf("после перерисовки фокус не в поле поиска, а на %s", activeElement(t, ctx))
	}
	// и колонка при этом цела: состав не менялся от запроса
	var count int
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelectorAll('#panel .panel-row').length`, &count)); err != nil {
		t.Fatal(err)
	}
	want := 1
	for _, group := range settingsGroups {
		want += len(group.Sections)
	}
	if count != want {
		t.Errorf("с запросом в колонке %d строк, а должно быть %d: состав колонки не зависит от поиска", count, want)
	}
}

// Масштаб 200 %: то же, что окно вдвое уже при том же кегле. Кнопки и
// значения не должны накрывать подписи — проверяем пересечения
// прямоугольников внутри каждой строки.
func TestBrowserZoom200(t *testing.T) {
	for _, screen := range []string{"settings:mail", "tasks", "health", "settings:models"} {
		ctx, отмена, _ := openInChrome(t, 660, 900, screen)
		var беды []string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
			const беды = [];
			const пересекаются = (a, b) => !(a.right <= b.left + 1 || b.right <= a.left + 1 || a.bottom <= b.top + 1 || b.bottom <= a.top + 1);
			document.querySelectorAll('.row').forEach((стр) => {
				const подписи = Array.from(стр.querySelectorAll('.title, .label, .caption')).map((э) => [э, э.getBoundingClientRect()]).filter(([, р]) => р.width > 0);
				const управление = Array.from(стр.querySelectorAll('.action, .switch, .pill, .value')).map((э) => [э, э.getBoundingClientRect()]).filter(([, р]) => р.width > 0);
				for (const [п, пр] of подписи) for (const [у, ур] of управление) {
					if (!п.contains(у) && !у.contains(п) && пересекаются(пр, ур)) {
						беды.push((п.textContent || '').trim().slice(0, 30) + ' ⟷ ' + (у.textContent || у.className).toString().trim().slice(0, 30));
						return;
					}
				}
			});
			return беды.slice(0, 6);
		})()`, &беды)); err != nil {
			отмена()
			t.Fatal(err)
		}
		for _, б := range беды {
			t.Errorf("%s при 200 %%: наезд — %s", screen, б)
		}
		отмена()
	}
}
