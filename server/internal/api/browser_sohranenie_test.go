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

// Нижняя полоса сохранения и защита перехода — настоящими нажатиями.
//
// Проверять такое вызовом функции из консоли бессмысленно: сборка
// разметки бывает безупречной, а живое нажатие не работает — на этом уже
// горели с кнопкой меню на телефоне, которую собственный обработчик
// закрывал тем же кликом. Здесь человек тыкает тумблер, потом раздел в колонке,
// потом «Остаться» и «Сохранить» — ровно как за столом.
func TestBrowserSavingBarAndLeaving(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 120*time.Second)
	defer cancelTime()

	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1100, 820),
		// «Общее» у «Почты»: первая форма — «Заходить в спам и корзину сервера»
		chromedp.Navigate(srv.URL+"/#screen=settings:mail~common")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	var ready bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`!!document.querySelector('form.deferred .field-toggle')`, &ready,
		chromedp.WithPollingTimeout(20*time.Second))); err != nil || !ready {
		t.Fatalf("экран настроек почты не нарисовался: %v", err)
	}
	// полосы не видно, пока ничего не тронуто: пустая полоса внизу
	// обещала бы работу, которой нет
	var видна bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`!document.getElementById('save-bar').hidden`, &видна)); err != nil {
		t.Fatal(err)
	}
	if видна {
		t.Error("полоса сохранения стоит внизу, хотя ничего не изменено")
	}

	// человек переключил тумблер настоящим нажатием
	if err := chromedp.Run(ctx, chromedp.Click(`form.deferred .field-toggle`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать тумблер: %v", err)
	}
	var label string
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const п = document.getElementById('save-bar');
		  return !п.hidden ? п.querySelector('.what').textContent : ''; })()`,
		&label, chromedp.WithPollingTimeout(10*time.Second))); err != nil {
		t.Fatalf("полоса не появилась после правки: %v", err)
	}
	if !strings.Contains(label, "Изменено 1") {
		t.Errorf("полоса говорит %q — счёт правок не виден", label)
	}
	if !strings.Contains(label, "включено") && !strings.Contains(label, "выключено") {
		t.Errorf("полоса говорит %q, а должна называть, что именно изменилось", label)
	}

	// нажали другой раздел в колонке: экран остаётся, всплывает местная панель
	if err := chromedp.Run(ctx, chromedp.Click(`#panel .item[hx-get="/ui?screen=`+tgScreen+`"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать раздел в колонке: %v", err)
	}
	var ушёл string
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const у = document.getElementById('leave'); return у && !у.hidden ? у.querySelector('.what').textContent : ''; })()`,
		&ушёл, chromedp.WithPollingTimeout(10*time.Second))); err != nil {
		t.Fatalf("панель ухода не появилась: %v", err)
	}
	if !strings.Contains(ушёл, "пропадёт") {
		t.Errorf("панель ухода говорит %q — не сказано, что правка пропадёт", ушёл)
	}
	var screen string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('screen').dataset.screen`, &screen)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(screen, "settings:mail") {
		t.Fatalf("экран уехал на %q, хотя правка не сохранена", screen)
	}

	// «Остаться» — панель уходит, правка на месте
	if err := chromedp.Run(ctx, chromedp.Click(`#leave [data-leave="stay"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Остаться»: %v", err)
	}
	var осталась bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`document.getElementById('leave').hidden && !document.getElementById('save-bar').hidden`,
		&осталась, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !осталась {
		t.Fatalf("после «Остаться» полоса с правкой должна была остаться: %v", err)
	}

	// «Сохранить» — правка уходит на сервер, полоса гаснет, тумблер
	// остаётся в новом положении: значение приехало от Сёмы, а не из
	// памяти браузера (экран перечитывается целиком)
	if err := chromedp.Run(ctx, chromedp.Click(`#save-bar [data-save="save"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «Сохранить»: %v", err)
	}
	var сохранилось bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const п = document.getElementById('save-bar');
		  const ф = document.querySelector('form.deferred input[type=checkbox]');
		  return !!п && п.hidden && !!ф && !ф.checked && !ф.defaultChecked; })()`,
		&сохранилось, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !сохранилось {
		var что string
		_ = chromedp.Run(ctx, chromedp.Evaluate(
			`(document.querySelector('.not-saved:not([hidden])')||{}).textContent||''`, &что))
		t.Fatalf("сохранение не прошло (%v), у формы написано %q", err, что)
	}

	// сохранённое больше не держит: тот же раздел открывается сразу
	if err := chromedp.Run(ctx, chromedp.Click(`#panel .item[hx-get="/ui?screen=`+tgScreen+`"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать раздел в колонке: %v", err)
	}
	var уехал bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`!document.getElementById('screen').dataset.screen.startsWith('settings:mail')`, &уехал,
		chromedp.WithPollingTimeout(10*time.Second))); err != nil || !уехал {
		t.Fatalf("без несохранённого переход должен идти сразу: %v", err)
	}
}

// На телефоне раздел — целый экран с «‹ Настройки» над заголовком, и
// вернуться в корень можно, не сохранив правку. Панель ухода обязана быть
// видна: человек увидел бы, что нажатие ничего не сделало, и нажал бы ещё
// раз. (До 1 октября 2026 на телефоне разделы приезжали шторкой снизу, и
// панель всплывала за ней.)
func TestBrowserPhoneLeavingByBack(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTime()

	var ready bool
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(390, 844, chromedp.EmulateScale(2)),
		chromedp.Navigate(srv.URL+"/#screen=settings:mail~common"),
		chromedp.Poll(`!!document.querySelector('form.deferred .field-toggle')`, &ready,
			chromedp.WithPollingTimeout(20*time.Second))); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("экран не нарисовался: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`form.deferred .field-toggle`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать тумблер: %v", err)
	}
	var has bool
	if err := chromedp.Run(ctx, chromedp.Poll(`!document.getElementById('save-bar').hidden`, &has,
		chromedp.WithPollingTimeout(10*time.Second))); err != nil || !has {
		t.Fatalf("полоса сохранения не появилась: %v", err)
	}
	// «‹ Настройки» — настоящее нажатие
	if err := chromedp.Run(ctx, chromedp.Click(`.crumbs .crumb`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать «назад»: %v", err)
	}
	var shown bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const bar = document.getElementById('leave'); return !!bar && !bar.hidden; })()`,
		&shown, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !shown {
		var state string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`JSON.stringify({leave: !document.getElementById('leave').hidden,
		  screen: document.getElementById('screen').dataset.screen})`, &state))
		t.Fatalf("вопрос об уходе не виден: %s (%v)", state, err)
	}
	var screen string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('screen').dataset.screen`, &screen)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(screen, "settings:mail") {
		t.Fatalf("экран уехал на %q, хотя правка не сохранена", screen)
	}
}

// Правка не исчезает под руками. Провод перечитывает экран, как только
// на сервере что-то изменилось, и подменяет ленту целиком — вместе с
// формой, которую человек сейчас правит. Пока правка не сохранена и не
// отменена, экран остаётся тем, что человек видит.
func TestBrowserWireDoesNotWipeUnsaved(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTime()

	var ready bool
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1100, 820),
		chromedp.Navigate(srv.URL+"/#screen=settings:mail~common"),
		chromedp.Poll(`!!document.querySelector('form.deferred .field-toggle')`, &ready,
			chromedp.WithPollingTimeout(20*time.Second))); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("экран не нарисовался: %v", err)
	}
	// провод открывается после первого экрана; даём ему секунду
	time.Sleep(time.Second)
	if err := chromedp.Run(ctx, chromedp.Click(`form.deferred .field-toggle`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать тумблер: %v", err)
	}
	var правка bool
	if err := chromedp.Run(ctx, chromedp.Poll(`!document.getElementById('save-bar').hidden`,
		&правка, chromedp.WithPollingTimeout(10*time.Second))); err != nil || !правка {
		t.Fatalf("полоса сохранения не появилась: %v", err)
	}

	// человек убрал руку с тумблера: пока курсор стоит в поле,
	// перечитывание и так откладывается — проверяем не это
	if err := chromedp.Run(ctx, chromedp.Click(`.header h1`, chromedp.ByQuery)); err != nil {
		t.Fatalf("нажать мимо формы: %v", err)
	}
	var вПоле bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`!!document.activeElement && /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName)`, &вПоле)); err != nil {
		t.Fatal(err)
	}
	if вПоле {
		t.Fatal("курсор остался в поле — проверять нечего")
	}

	// на сервере изменилось ровно то, что открыто у человека
	s.eng.Changed("settings:mail~common")
	time.Sleep(2 * time.Second)

	var цело bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(() => { const ф = document.querySelector('form.deferred input[type=checkbox]');
		  return !!ф && !ф.checked && !document.getElementById('save-bar').hidden; })()`, &цело)); err != nil {
		t.Fatal(err)
	}
	if !цело {
		t.Fatal("перечитывание экрана по проводу стёрло несохранённую правку")
	}
}
