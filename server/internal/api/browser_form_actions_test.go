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

// Ряд кнопок формы не липнет к последнему полю (снимок владельца «Добавить
// сервер MCP» на «Интеграциях»): под последним полем шла линия-разделитель,
// а сразу под ней, вплотную, кнопка «Добавить и проверить».
//
// Причин было две. Кнопки лежали в `right-side` — общем классе правой части
// строки списка, без отступа, со стилем прямо в разметке. А правило «у
// последнего поля нет нижней линии» стояло как `:last-of-type`, который
// считает по типу элемента: последним `div` формы был ряд кнопок, а не
// поле, и линия оставалась.
//
// Меряется настоящая вёрстка, а не разметка: расстояние считается из
// getBoundingClientRect, граница — из getComputedStyle.
func TestBrowserFormActionsDoNotStickToLastField(t *testing.T) {
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

	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(srv.URL+"/#screen=settings:integrations@new")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	waitScreen(t, ctx, "settings:integrations@new")

	точек := func(п *float64) string {
		if п == nil {
			return "—"
		}
		return strconv.FormatFloat(*п, 'f', 1, 64)
	}
	// отступ от низа последнего поля до кнопки, меньше которого нельзя:
	// 8 точек — шаг сетки проекта (gap рядов кнопок, margin блоков)
	const отступ = 8.0

	type замер struct {
		Found        bool     `json:"found"`
		Fields       int      `json:"fields"`
		Buttons      int      `json:"buttons"`
		RowClass     string   `json:"rowClass"`
		RowWrap      string   `json:"rowWrap"`
		RowJustify   string   `json:"rowJustify"`
		GapBox       float64  `json:"gapBox"`
		GapContent   float64  `json:"gapContent"`
		LastBorder   string   `json:"lastBorder"`
		BetweenLine  float64  `json:"betweenLine"`
		BtnHeight    float64  `json:"btnHeight"`
		BtnLeft      float64  `json:"btnLeft"`
		BtnGap       *float64 `json:"btnGap"`
		RightOverrun float64  `json:"rightOverrun"`
		TextOverrun  float64  `json:"textOverrun"`
		Rows         int      `json:"rows"`
		RowsGap      *float64 `json:"rowsGap"`
		PageOverflow float64  `json:"pageOverflow"`
	}
	// измерить — форма по заголовку. Закрытые раскрытия вокруг неё
	// открываются: в закрытом всё меряется нулями. С длинными кнопками
	// (`многие`) в ряд добавляются копии с длинными подписями — проверка,
	// что ряд на узкой колонке переносится и не вылезает за край
	измерить := func(заголовок string, многие bool) замер {
		t.Helper()
		var м замер
		js := `((заголовок, многие) => {
			const формы = [...document.querySelectorAll('form.form')];
			// форма без заголовка (экран «Добавить сервер MCP» — свой, заголовок у экрана)
			// ищется по полю: «поле:<имя>»
			const ф = заголовок.startsWith('поле:')
				? формы.find((x) => !!x.querySelector('[name="' + заголовок.slice(5) + '"]'))
				: формы.find((x) => { const h = x.querySelector('h3'); return h && h.textContent.trim() === заголовок; });
			if (!ф) return {found: false};
			for (let d = ф.closest('details'); d; d = d.parentElement && d.parentElement.closest('details')) d.open = true;
			// «Ещё N полей» внутри самой формы тоже раскрывается: свёрнутое поле не имеет размера,
			// и расстояние до него ничего не говорит
			ф.querySelectorAll('details').forEach((d) => { d.open = true; });
			const кнопки0 = [...ф.querySelectorAll('button[type=submit]')];
			if (!кнопки0.length) return {found: false};
			const ряд = кнопки0[0].parentElement;
			if (многие) {
				for (let i = 0; i < 3; i++) {
					const к = кнопки0[0].cloneNode(true);
					к.textContent = i === 2
						? 'Совсем длинная подпись кнопки, которая сама не поместится в одну строку узкой колонки телефона'
						: 'Очень длинное название кнопки номер ' + (i + 1);
					ряд.appendChild(к);
				}
			}
			const кнопки = [...ф.querySelectorAll('button[type=submit]')];
			const поля = [...ф.querySelectorAll('.field')];
			const последнее = поля[поля.length - 1];
			const рф = ф.getBoundingClientRect();
			const рк = кнопки[0].getBoundingClientRect();
			// низ последнего блока над рядом: поле, а если формы без полей — заголовок
			const над = последнее || ф.querySelector('h3');
			const рн = над.getBoundingClientRect();
			let низСодержимого = рн.bottom;
			if (последнее) низСодержимого = Math.max(...[...последнее.children].map((c) => c.getBoundingClientRect().bottom));
			const число = (v) => parseFloat(v) || 0;
			const между = поля.length > 1
				? число(getComputedStyle(поля[0]).borderBottomWidth) + число(getComputedStyle(поля[1]).borderTopWidth)
				: -1;
			const цк = кнопки.map((к) => к.getBoundingClientRect());
			const верхи = [...new Set(цк.map((р) => Math.round(р.top)))].sort((a, b) => a - b);
			let зазорРядов = null;
			if (верхи.length > 1) {
				const первыйРяд = цк.filter((р) => Math.round(р.top) === верхи[0]);
				const второй = цк.filter((р) => Math.round(р.top) === верхи[1]);
				зазорРядов = Math.min(...второй.map((р) => р.top)) - Math.max(...первыйРяд.map((р) => р.bottom));
			}
			return {
				found: true,
				fields: поля.length,
				buttons: кнопки.length,
				rowClass: ряд.className,
				rowWrap: getComputedStyle(ряд).flexWrap,
				rowJustify: getComputedStyle(ряд).justifyContent,
				gapBox: рк.top - рн.bottom,
				gapContent: рк.top - низСодержимого,
				lastBorder: последнее ? getComputedStyle(последнее).borderBottomWidth : '',
				betweenLine: между,
				btnHeight: рк.height,
				btnLeft: рк.left - рф.left,
				btnGap: кнопки.length > 1 && верхи.length === 1 ? цк[1].left - цк[0].right : null,
				rightOverrun: Math.max(...цк.map((р) => р.right)) - рф.right,
				// подпись, выпирающая из своей кнопки: рамка кнопки в колонке, а текст за ней
				textOverrun: Math.max(...кнопки.map((к) => к.scrollWidth - к.clientWidth)),
				rows: верхи.length,
				rowsGap: зазорРядов,
				pageOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
			};
		})(` + jsString(заголовок) + `, ` + strconv.FormatBool(многие) + `)`
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, &м)); err != nil {
			t.Fatal(err)
		}
		return м
	}
	// ждать, пока форма появится на странице (условие, а не срок)
	ждатьФорму := func(заголовок string) {
		t.Helper()
		var ready bool
		if err := chromedp.Run(ctx, chromedp.Poll(
			`((з) => [...document.querySelectorAll('form.form')].some((f) => { if (з.startsWith('поле:')) return !!f.querySelector('[name="' + з.slice(5) + '"]');
				const h = f.querySelector('h3'); return !!h && h.textContent.trim() === з; }))(`+jsString(заголовок)+`)`,
			&ready, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !ready {
			t.Fatalf("формы %q на экране нет: %v", заголовок, err)
		}
	}
	проверить := func(метка, заголовок string, ждётКнопок int) {
		t.Helper()
		м := измерить(заголовок, false)
		if !м.Found {
			t.Fatalf("%s: форма %q не найдена", метка, заголовок)
		}
		t.Logf("%s: полей %d, кнопок %d, класс ряда %q, от низа поля до кнопки %.1f, от содержимого поля %.1f, нижняя линия последнего поля %q, линия между полями %.0f, рост кнопки %.0f, левый край %.0f, промежуток кнопок %s",
			метка, м.Fields, м.Buttons, м.RowClass, м.GapBox, м.GapContent, м.LastBorder, м.BetweenLine, м.BtnHeight, м.BtnLeft, точек(м.BtnGap))
		if м.Buttons != ждётКнопок {
			t.Errorf("%s: кнопок %d, ждали %d", метка, м.Buttons, ждётКнопок)
		}
		if !strings.Contains(" "+м.RowClass+" ", " form-actions ") {
			t.Errorf("%s: ряд кнопок с классом %q, а не form-actions — общий right-side строки списка", метка, м.RowClass)
		}
		if м.GapBox < отступ {
			t.Errorf("%s: от низа последнего поля до кнопки %.1f, меньше %.0f — кнопка липнет", метка, м.GapBox, отступ)
		}
		if м.GapContent < 2*отступ {
			t.Errorf("%s: от последней строки поля до кнопки %.1f, меньше %.0f", метка, м.GapContent, 2*отступ)
		}
		if м.LastBorder != "0px" {
			t.Errorf("%s: у последнего поля нижняя граница %q, ждали 0px — линия перед кнопками", метка, м.LastBorder)
		}
		if м.Fields > 1 && м.BetweenLine <= 0 {
			t.Errorf("%s: линии между полями пропали", метка)
		}
		if м.RowWrap != "wrap" || м.RowJustify != "flex-start" {
			t.Errorf("%s: ряд кнопок: flex-wrap %q, justify-content %q, ждали wrap и flex-start", метка, м.RowWrap, м.RowJustify)
		}
		if м.BtnHeight != 32 {
			t.Errorf("%s: рост кнопки %.0f, ждали --control-h = 32", метка, м.BtnHeight)
		}
		if м.BtnGap != nil && *м.BtnGap < 8 {
			t.Errorf("%s: промежуток между кнопками %.1f, меньше 8", метка, *м.BtnGap)
		}
		if м.BtnLeft > 40 {
			t.Errorf("%s: кнопки уехали вправо: %.0f от края формы", метка, м.BtnLeft)
		}
	}

	// одна кнопка: «Добавить и проверить» на экране «Добавить сервер MCP»
	ждатьФорму("поле:url")
	проверить("Интеграции, «Добавить сервер MCP»", "поле:url", 1)

	// две кнопки: «Поиск страниц (SearXNG)» — «Сохранить» и «Проверить»
	if err := chromedp.Run(ctx, chromedp.Evaluate(`location.hash = '#screen=settings:search'`, nil)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, "settings:search")
	ждатьФорму("Поиск страниц (SearXNG)")
	проверить("Поиск, «Поиск страниц (SearXNG)»", "Поиск страниц (SearXNG)", 2)

	// мастер: та же форма с шагами, ряд кнопок под последним шагом
	if err := chromedp.Run(ctx, chromedp.Evaluate(`location.hash = '#screen=tasks'`, nil)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, "tasks")
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`[...document.querySelectorAll('button.action')].find((к) => к.textContent.trim() === 'Новая задача').click()`, nil)); err != nil {
		t.Fatal(err)
	}
	var мастер struct {
		Found    bool    `json:"found"`
		RowClass string  `json:"rowClass"`
		Gap      float64 `json:"gap"`
		Left     float64 `json:"left"`
		Lines    float64 `json:"lines"`
	}
	var готов bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`!!document.querySelector('form.wizard button[type=submit]')`, &готов,
		chromedp.WithPollingTimeout(15*time.Second))); err != nil || !готов {
		t.Fatalf("мастер «Новая задача» не открылся: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const ф = document.querySelector('form.wizard');
		const к = ф.querySelector('button[type=submit]');
		const шаги = [...ф.querySelectorAll('fieldset.step')];
		const низ = шаги[шаги.length - 1].getBoundingClientRect().bottom;
		const поля = [...ф.querySelectorAll('fieldset.step .field')];
		// последнее поле шага не несёт нижней линии
		const посл = поля[поля.length - 1];
		return {found: true, rowClass: к.parentElement.className,
			gap: к.getBoundingClientRect().top - низ, left: к.getBoundingClientRect().left - ф.getBoundingClientRect().left,
			lines: parseFloat(getComputedStyle(посл).borderBottomWidth) || 0};
	})()`, &мастер)); err != nil {
		t.Fatal(err)
	}
	t.Logf("мастер: класс ряда %q, от последнего шага до кнопки %.1f, левый край %.0f, нижняя линия последнего поля %.0f", мастер.RowClass, мастер.Gap, мастер.Left, мастер.Lines)
	if !strings.Contains(" "+мастер.RowClass+" ", " form-actions ") {
		t.Errorf("мастер: ряд кнопок с классом %q, а не form-actions", мастер.RowClass)
	}
	if мастер.Gap < отступ {
		t.Errorf("мастер: от последнего шага до кнопки %.1f, меньше %.0f", мастер.Gap, отступ)
	}
	if мастер.Lines != 0 {
		t.Errorf("мастер: у последнего поля нижняя линия %.0f", мастер.Lines)
	}

	// форма без своих кнопок (её сохраняет нижняя полоса): последним `div`
	// стоит скрытый блок «не сохранено», а не поле — то же место, где
	// `:last-of-type` не срабатывал. Линия под последним полем не нужна и тут
	// «Почта» с 2 октября 2026: формы — на вкладках раздела, а не на списке ящиков
	if err := chromedp.Run(ctx, chromedp.Evaluate(`location.hash = '#screen=settings:mail~common'`, nil)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, "settings:mail~common")
	var отложенная struct {
		Fields  int    `json:"fields"`
		Border  string `json:"border"`
		Buttons int    `json:"buttons"`
	}
	var есть bool
	if err := chromedp.Run(ctx, chromedp.Poll(`!!document.querySelector('form.form.deferred .field')`, &есть,
		chromedp.WithPollingTimeout(15*time.Second))); err != nil || !есть {
		t.Fatalf("на «Почте» нет формы, которую сохраняет нижняя полоса: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const ф = document.querySelector('form.form.deferred');
		const поля = [...ф.querySelectorAll('.field')];
		return {fields: поля.length, border: getComputedStyle(поля[поля.length - 1]).borderBottomWidth,
			buttons: ф.querySelectorAll('button').length};
	})()`, &отложенная)); err != nil {
		t.Fatal(err)
	}
	t.Logf("форма нижней полосы: полей %d, нижняя граница последнего %q, кнопок в форме %d", отложенная.Fields, отложенная.Border, отложенная.Buttons)
	if отложенная.Border != "0px" {
		t.Errorf("форма нижней полосы: у последнего поля нижняя граница %q, ждали 0px", отложенная.Border)
	}
	if отложенная.Buttons != 0 {
		t.Errorf("форма нижней полосы получила кнопки (%d): её сохраняет полоса", отложенная.Buttons)
	}

	// телефон: колонка узкая, кнопки переносятся и не вылезают за край.
	// Из-за 320 точек ряд из четырёх длинных подписей в одну строку не встанет.
	// Сужение само перечитывает текущий экран телефонным документом, и переход по
	// адресу в тот же миг с ним гоняется — побеждает то, что приехало позже; поэтому
	// сперва ждём, пока телефонный документ приедет («‹ Настройки» над заголовком)
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(320, 700, chromedp.EmulateMobile)); err != nil {
		t.Fatal(err)
	}
	var телефонный bool
	if err := chromedp.Run(ctx, chromedp.Poll(
		`(() => { const к = document.querySelector('#screen .crumbs .crumb'); return !!к && к.textContent.includes('Настройки'); })()`,
		&телефонный, chromedp.WithPollingTimeout(15*time.Second))); err != nil || !телефонный {
		t.Fatalf("после сужения окна телефонный документ не приехал: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`location.hash = '#screen=settings:search'`, nil)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, "settings:search")
	ждатьФорму("Поиск страниц (SearXNG)")
	м := измерить("Поиск страниц (SearXNG)", true)
	t.Logf("телефон 320: кнопок %d, рядов %d, зазор между рядами %s, высота кнопки %.0f, выступ за правый край формы %.1f, подпись за краем кнопки %.0f, выступ страницы вбок %.0f, от поля до кнопки %.1f",
		м.Buttons, м.Rows, точек(м.RowsGap), м.BtnHeight, м.RightOverrun, м.TextOverrun, м.PageOverflow, м.GapBox)
	if м.Rows < 2 {
		t.Errorf("телефон: %d кнопок стоят в %d ряд — не переносятся", м.Buttons, м.Rows)
	}
	if м.RowsGap == nil || *м.RowsGap < 8 {
		t.Errorf("телефон: зазор между рядами кнопок %s, меньше 8 — ряды липнут", точек(м.RowsGap))
	}
	if м.RightOverrun > 0.5 {
		t.Errorf("телефон: кнопки вылезли за правый край формы на %.1f", м.RightOverrun)
	}
	if м.TextOverrun > 0 {
		t.Errorf("телефон: длинная подпись выпирает из кнопки на %.0f — не переносится", м.TextOverrun)
	}
	if м.PageOverflow > 1 {
		t.Errorf("телефон: страница уехала вбок на %.0f", м.PageOverflow)
	}
	if м.GapBox < отступ || м.LastBorder != "0px" {
		t.Errorf("телефон: от поля до кнопки %.1f, нижняя линия последнего поля %q", м.GapBox, м.LastBorder)
	}
}
