package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// Версия сборки подставляется в работник и в оболочку одинаково: иначе
// имя кэша и проверка «оболочка устарела» разойдутся между собой.
func TestBuildVersionStamped(t *testing.T) {
	if len(Build()) != 12 {
		t.Fatalf("версия сборки: %q", Build())
	}
	h := Handler()
	for _, name := range []string{"/sw.js", "/app.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", name, nil))
		body := rec.Body.String()
		if strings.Contains(body, buildMark) {
			t.Errorf("%s: метка версии осталась не подставленной", name)
		}
		if !strings.Contains(body, Build()) {
			t.Errorf("%s: версии сборки %q в файле нет", name, Build())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Errorf("%s: Content-Type %q", name, ct)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/sw.js", nil))
	if !strings.Contains(rec.Body.String(), "'сёма-оболочка-"+Build()+"'") {
		t.Errorf("имя кэша работника без версии сборки")
	}
}

// Групповое действие в браузере: у отмечаемой строки — свой флажок, а
// кнопка с целью «$selected» забирает отметки в запрос. Без этого кнопка
// «В спам выбранные» отправляла на сервер строку «$selected», и человек
// получал «strconv.ParseInt».
func TestRowSelectionForGroupAction(t *testing.T) {
	scr := Screen{Blocks: []Block{{
		Kind: "раздел", Title: "Похоже на спам",
		Buttons: []Button{{Action: "confirm_spam", Target: "$selected", Name: "В спам выбранные"}},
		Children: []Block{
			{Kind: "строка", ID: "1695", Name: "Письмо", Selectable: true},
			{Kind: "строка", ID: "1696", Name: "Другое"},
		},
	}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	if !strings.Contains(got, `name="выбрано" value="1695"`) {
		t.Error("у отмечаемой строки нет флажка")
	}
	if strings.Contains(got, `value="1696"`) {
		t.Error("флажок появился у строки, которую не отмечают")
	}
	if !strings.Contains(got, `hx-include=".row-check input:checked"`) {
		t.Error("кнопка группового действия не забирает отметки")
	}
}

// Таблица: подпись колонки печатается один раз наверху, а не в каждой
// строке; машинный факт идёт моноширинным столбцом, управление строки —
// последней ячейкой. До этого «наружу» стояло у каждой из тридцати строк.
func TestTableWithColumnLabels(t *testing.T) {
	scr := Screen{Blocks: []Block{{
		Kind: "таблица", Title: "Области памяти",
		Columns: []string{"Область", "Записей", "Наружу", ""},
		Children: []Block{{
			Kind: "строка", Name: "работа", Caption: "memory:node:1",
			Cells: []Cell{
				{Text: "128", Mono: true},
				{Text: "закрыто", Level: "warn"},
			},
			Toggle:  "on",
			Buttons: []Button{{Action: "toggle_area", Target: "работа", Name: "toggle"}},
		}},
	}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	// видимо подпись колонки одна — в шапке. В ячейке она лежит атрибутом
	// data-label ради телефона, где шапки нет и строка раскладывается карточкой
	visible := regexp.MustCompile(` data-label="[^"]*"`).ReplaceAllString(got, "")
	if strings.Count(visible, "Наружу") != 1 {
		t.Errorf("подпись колонки печатается %d раз", strings.Count(visible, "Наружу"))
	}
	if !strings.Contains(got, `data-label="Наружу"`) {
		t.Error("у ячейки нет подписи колонки для телефона")
	}
	if !strings.Contains(got, `<th>Записей</th>`) {
		t.Error("нет заголовка колонки")
	}
	if !strings.Contains(got, `class="table-cell mono"`) {
		t.Error("машинный факт не помечен моноширинным")
	}
	if !strings.Contains(got, `class="table-cell warn"`) {
		t.Error("уровень ячейки потерян")
	}
	if !strings.Contains(got, `role="switch"`) {
		t.Error("тумблер строки не доехал до таблицы")
	}
}

// Ряд капсул не растёт бесконечно: видимые капсулы плюс честный хвост
// «ещё N». Пятьдесят четыре модели занимали семь строк экрана.
func TestPillRowWithTail(t *testing.T) {
	scr := Screen{Blocks: []Block{{
		Kind: "капсулы-ряд", Title: "Круг чтения",
		Pills: []Pill{{Text: "uptime"}, {Text: "df"}},
		More:  "ещё 74",
	}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	if !strings.Contains(got, `class="pill set "`) {
		t.Error("капсулы ряда без своего оформления")
	}
	if !strings.Contains(got, "ещё 74") {
		t.Error("хвост не сказал, сколько осталось")
	}
}

// Шаг мастера: три состояния видно по разметке, содержимое лежит внутри
// своего шага, а не общим куском под списком.
func TestWizardStepThreeStates(t *testing.T) {
	scr := Screen{Blocks: []Block{
		{Kind: "шаг", Style: "done", Text: "1", Name: "Почта", Caption: "owner@example.org"},
		{Kind: "шаг", Style: "open", Text: "2", Name: "Модель",
			Children: []Block{{Kind: "текст", Text: "какую модель звать"}}},
		{Kind: "шаг", Style: "next", Text: "3", Name: "Ключ"},
	}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	for _, cls := range []string{"wizard-step done", "wizard-step open", "wizard-step next"} {
		if !strings.Contains(got, cls) {
			t.Errorf("нет состояния %q", cls)
		}
	}
	if !strings.Contains(got, "step-body") || !strings.Contains(got, "какую модель звать") {
		t.Error("содержимое раскрытого шага не внутри шага")
	}
}

// Обратный путь стоит в шапке, над заголовком: последнее звено — где мы
// стоим, остальные ведут назад.
func TestBreadcrumbInHeader(t *testing.T) {
	scr := Screen{
		Title:      "Настройки",
		Breadcrumb: []Button{{Action: "$goto", Target: "settings", Name: "Настройки"}},
		Here:       "Почта",
	}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	path := strings.Index(got, `class="crumbs"`)
	h1 := strings.Index(got, "<h1") // с tabindex: после перехода фокус встаёт на заголовок
	if path < 0 || h1 < 0 || path > h1 {
		t.Errorf("путь не стоит над заголовком: путь %d, заголовок %d", path, h1)
	}
	if !strings.Contains(got, `hx-get="/ui?screen=settings"`) {
		t.Error("звено пути никуда не ведёт")
	}
	if !strings.Contains(got, `aria-current="page"`) {
		t.Error("текущее место не помечено")
	}
}

// Свой пузырь и «Сёма от вашего имени» стоят справа оба, и различать их
// цветом нельзя: на телефоне при солнце цвет не читается. Различие — в
// рамке: сплошная у своего, пунктирная у Сёминого. Проверяем и разметку
// (роль доехала), и само оформление: без него оба пузыря одинаковые.
func TestBubbleSentOnYourBehalfDashed(t *testing.T) {
	scr := Screen{Blocks: []Block{
		{Kind: "пузырь", Role: "свой", Text: "Сейчас посмотрю.", Meta: "вы · 17:52"},
		{Kind: "пузырь", Role: "ушло", Title: "Сёма от вашего имени",
			Text: "46 °C, нагрузка почти нулевая.", Meta: "отправлено · 17:54"},
	}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	if !strings.Contains(got, `class="bubble свой"`) || !strings.Contains(got, `class="bubble ушло"`) {
		t.Error("роли пузырей не доехали до разметки")
	}
	if !strings.Contains(got, "Сёма от вашего имени") {
		t.Error("подписи «кто написал» над пузырём нет")
	}
	if !strings.Contains(got, `class="who right"`) {
		t.Error("подпись правого пузыря встала слева")
	}
	// оформление: сплошная у своего, пунктирная у Сёминого
	css := siteStyle(t)
	own := cssRule(css, ".bubble.свой .text")
	sent := cssRule(css, ".bubble.ушло .text")
	if !strings.Contains(own, "solid") {
		t.Errorf("свой пузырь без сплошной рамки: %q", own)
	}
	if !strings.Contains(sent, "dashed") {
		t.Errorf("«от вашего имени» без пунктирной рамки: %q", sent)
	}
}

// siteStyle — оформление так, как его получит браузер: правки в файле
// без правок в разметке ничего не значат и наоборот.
func siteStyle(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/style.css", nil))
	if rec.Code != 200 {
		t.Fatalf("style.css отдался с кодом %d", rec.Code)
	}
	return rec.Body.String()
}

func cssRule(css, selector string) string {
	i := strings.Index(css, selector+" {")
	if i < 0 {
		return ""
	}
	j := strings.Index(css[i:], "}")
	if j < 0 {
		return ""
	}
	return css[i : i+j]
}

// Ожидание всегда именное: кто думает, какой моделью и сколько, плюс
// кнопка «Отменить». Локальная модель думает по четыре минуты, и
// крутящийся кружок без имени человеку ничего не говорит.
func TestWaitingNamesModel(t *testing.T) {
	scr := Screen{Blocks: []Block{{
		Kind: "ожидание", ID: "думает", Title: "Сёма читает файл", Meta: "DeepseekPro · 4 с",
		Buttons: []Button{{Action: "stop_chat", Target: "12", Name: "Отменить"}},
	}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	for _, want := range []string{"Сёма читает файл", "DeepseekPro · 4 с", "Отменить", `role="status"`} {
		if !strings.Contains(got, want) {
			t.Errorf("в ожидании нет %q", want)
		}
	}
}

// Карточка дела: полоска уровня, значок, моноширинное тело и ряд
// действий, где разрушительное отделено пустотой. «Отклонить» вплотную к
// «Выполнить» нажимается сгоряча, а отменить выполненную команду нечем.
func TestTaskCardSeparatesDestructive(t *testing.T) {
	scr := Screen{Blocks: []Block{{
		Kind: "карточка-дела", ID: "ssh-7", Level: "warn",
		RowIcon: "terminal",
		Title:   "Команда на сервере", Meta: "srv-bots · сегодня 08:02",
		Pills:   []Pill{{Text: "ждёт вас", Level: "warn"}},
		Output:  "getent passwd | awk -F: '$3>=1000'",
		Caption: "Просил модуль «Серверы под присмотром».",
		Buttons: []Button{
			{Action: "approve_ssh", Target: "7", Name: "Выполнить", Kind: "primary"},
			{Action: "always_ssh", Target: "7", Name: "Выполнить и впредь без вопроса"},
			{Action: "reject_ssh", Target: "7", Name: "Отклонить", Kind: "destructive"},
		},
	}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	if !strings.Contains(got, `class="matter level-warn"`) {
		t.Error("уровень карточки не доехал полоской")
	}
	if !strings.Contains(got, `class="level-bar"`) {
		t.Error("нет полоски уровня")
	}
	if !strings.Contains(got, `class="matter-code"`) {
		t.Error("команда набрана не моноширинным телом")
	}
	gapAt := strings.Index(got, `class="spacer"`)
	runAt := strings.Index(got, "Выполнить<")
	rejectAt := strings.Index(got, "Отклонить<")
	if gapAt < 0 || runAt < 0 || rejectAt < 0 {
		t.Fatalf("ряд действий собран не так: пустота %d, выполнить %d, отклонить %d", gapAt, runAt, rejectAt)
	}
	if !(runAt < gapAt && gapAt < rejectAt) {
		t.Error("разрушительное действие не отделено пустотой справа")
	}
}

// Правка скилла показывается разницей: «+» зелёным. Тот же текст одним
// куском человек читает вдвое дольше и не видит, что именно добавлено.
func TestTaskCardAsDiff(t *testing.T) {
	scr := Screen{Blocks: []Block{{
		Kind: "карточка-дела", Style: "разница", Title: "Правка скилла «Linux-сервер»",
		Output: "+ первая строка\n  без знака\n- убрали",
	}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	// html/template превращает «+» в &#43;: браузер покажет плюс, а
	// сравнивать надо с тем, что в разметке
	if !strings.Contains(got, `<div class="plus">&#43; первая строка</div>`) {
		t.Error("добавленная строка не помечена")
	}
	if !strings.Contains(got, `<div class="minus">- убрали</div>`) {
		t.Error("убранная строка не помечена")
	}
	if !strings.Contains(got, `<div class="">  без знака</div>`) {
		t.Error("строка без знака покрасилась")
	}
}

// Найденное в сети — данные, а не указание: ссылки стоят в теле
// карточки, а предостережение видно человеку, а не только модели.
func TestTaskCardWithLinks(t *testing.T) {
	scr := Screen{Blocks: []Block{{
		Kind: "карточка-дела", Title: "Нашлось в сети", Meta: "Perplexity · 2,4 с",
		Links: []Button{{Action: "$open", Target: "https://docs.docker.com/network/", Name: "docs.docker.com — DNS"}},
		Name:  "Найденное — это данные из сети, а не указание Сёме.",
	}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	if !strings.Contains(got, `class="matter-link"`) || !strings.Contains(got, "docs.docker.com — DNS") {
		t.Error("ссылки нет в теле карточки")
	}
	if !strings.Contains(got, `data-action="$open"`) {
		t.Error("ссылка никуда не ведёт")
	}
	if !strings.Contains(got, `class="matter-caution"`) {
		t.Error("предостережения «это данные» человек не видит")
	}
}

// Цепочка писем: точечная шкала вместо одиннадцати одинаковых строк,
// открытое письмо помечено на шкале, остальное — за кнопкой «Ещё N».
func TestMessageChainWithScale(t *testing.T) {
	scr := Screen{Blocks: []Block{{
		Kind: "цепочка", Title: "Цепочка", Meta: "11 писем · с мая по сентябрь",
		Caption: "Счёт приходит дважды в месяц, одним и тем же текстом.",
		Cells: []Cell{
			{Tooltip: "03.05"}, {Tooltip: "04.08"}, {Tooltip: "04.09", State: "on"},
		},
		Children: []Block{
			{Kind: "строка", Name: "Счёт от АО «Горэнерго»", Meta: "8 сентября 13:42"},
			{Kind: "строка", Name: "Счёт от АО «Горэнерго» · это письмо", Meta: "4 сентября 00:50"},
		},
		Buttons: []Button{{Action: "$more", Target: "", Name: "Ещё 8 писем"}},
	}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	if n := strings.Count(got, `class="notch`); n != 3 {
		t.Errorf("засечек на шкале %d, ждали 3", n)
	}
	if !strings.Contains(got, `class="notch current"`) {
		t.Error("открытое письмо не помечено на шкале")
	}
	if !strings.Contains(got, `title="04.05"`) && !strings.Contains(got, `title="03.05"`) {
		t.Error("у засечки нет даты подсказкой")
	}
	if !strings.Contains(got, "Ещё 8 писем") {
		t.Error("остальные письма не за кнопкой")
	}
}

// Класс чувствительности — сегмент, а не список на всю ширину, и рядом
// одна строка про то, что уходит наружу.
func TestSensitivityClassAsSegments(t *testing.T) {
	scr := Screen{Blocks: []Block{{
		Kind: "класс", RowIcon: "lock", Title: "Чувствительное", Text: "чувствительное",
		Caption: "Чувствительное уходит облачной модели; секретное не уходит никогда.",
		Buttons: []Button{
			{Action: "set_class", Target: "12|обычное", Name: "обычное"},
			{Action: "set_class", Target: "12|чувствительное", Name: "чувствительное"},
			{Action: "set_class", Target: "12|секретное", Name: "секретное"},
		},
	}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	if n := strings.Count(got, `<button class="segment`); n != 3 {
		t.Errorf("сегментов %d, ждали 3", n)
	}
	if !strings.Contains(got, `class="segment selected" aria-pressed="true"`) {
		t.Error("нынешний класс не отмечен")
	}
	if !strings.Contains(got, "не уходит никогда") {
		t.Error("строки про то, что уходит наружу, нет")
	}
}

// Переключатель сущностей: ящики карточками, нынешний помечен. Боковая
// панель на телефоне не живёт, а список прячет состояние ящика.
func TestEntitySwitcherAsCards(t *testing.T) {
	scr := Screen{Blocks: []Block{{
		Kind: "переключатель", Title: "Ящики",
		Children: []Block{
			{Kind: "строка", Style: "current", Name: "owner@example.org", Caption: "1 письмо к разбору", Level: "ok"},
			{Kind: "строка", Name: "owner@example.com", Caption: "молчит с 10:40", Level: "alert",
				GoTo: "inbox@owner@example.com"},
		},
	}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	if n := strings.Count(got, `class="entity`); n != 2 {
		t.Errorf("карточек сущностей %d, ждали 2", n)
	}
	if !strings.Contains(got, `class="entity selected" aria-current="true"`) {
		t.Error("нынешняя сущность не помечена")
	}
	if !strings.Contains(got, `hx-get="/ui?screen=inbox@owner@example.com"`) {
		t.Error("карточка соседнего ящика никуда не ведёт")
	}
	if !strings.Contains(got, "background:var(--alert)") {
		t.Error("состояние ящика не видно точкой")
	}
}

// demoFeed — образец ленты и письма на новом языке: все шесть вещей
// сразу. Служит и снимку в Chrome, и глазам: рассуждением такое не
// проверяется, а вёрстка ломается ровно там, где кажется, что всё просто.
func demoFeed() Screen {
	share := 0.0
	_ = share
	return Screen{
		Title:      "Сёма · Вадим",
		Subheading: "Ветка под разговором «Telegram · Вадим»",
		Width:      "read",
		Blocks: []Block{
			{Kind: "переключатель", Title: "Ящики", Children: []Block{
				{Kind: "строка", Style: "current", Name: "owner@example.org",
					Caption: "1 письмо к разбору", Level: "ok"},
				{Kind: "строка", Name: "owner@example.com", Caption: "молчит с 10:40",
					Level: "alert", GoTo: "inbox@owner@example.com"},
			}},
			{Kind: "класс", RowIcon: "lock", Title: "Чувствительное", Text: "чувствительное",
				Caption: "Сёма решил так сам: в письме лицевой счёт и сумма. Чувствительное уходит облачной модели по нынешней настройке; секретное не уходит никогда.",
				Buttons: []Button{
					{Action: "set_class", Target: "12|обычное", Name: "обычное"},
					{Action: "set_class", Target: "12|чувствительное", Name: "чувствительное"},
					{Action: "set_class", Target: "12|секретное", Name: "секретное"},
				}},
			{Kind: "пузырь", Role: "собеседник", Title: "Вадим",
				Text: "Скажи, какая температура процессора?", Meta: "17:52"},
			{Kind: "пузырь", Role: "свой", Text: "Сейчас посмотрю.", Meta: "вы · 17:52"},
			{Kind: "пузырь", Role: "сёма", Title: "Сёма · DeepseekPro",
				Text: "Сейчас 46 °C, нагрузка 0,07 — всё в порядке.", Meta: "17:52"},
			{Kind: "пузырь", Role: "ушло", Title: "Сёма от вашего имени",
				Text: "46 °C, нагрузка почти нулевая.", Meta: "отправлено · 17:54"},
			{Kind: "карточка-дела", Level: "warn", RowIcon: "terminal",
				Title: "Команда на сервере", Meta: "srv-bots · сегодня 08:02",
				Pills:   []Pill{{Text: "ждёт вас", Level: "warn"}},
				Output:  "getent passwd | awk -F: '$3>=1000 && $7!~/nologin|false/'",
				Caption: "Просил модуль «Серверы под присмотром». Сёма не выполняет сам: в команде есть перенаправление вывода — такое всегда идёт человеку.",
				Buttons: []Button{
					{Action: "approve_ssh", Target: "7", Name: "Выполнить", Kind: "primary"},
					{Action: "always_ssh", Target: "7", Name: "Выполнить и впредь без вопроса"},
					{Action: "reject_ssh", Target: "7", Name: "Отклонить", Kind: "destructive"},
				}},
			{Kind: "карточка-дела", RowIcon: "timer", Title: "Таймер",
				Meta: "поставил Сёма · до 12:40", CountdownTo: "2026-09-09T12:40:00+04:00",
				Caption: "Что проверить: «resets» — вернулся ли лимит у Perplexity.",
				Buttons: []Button{
					{Action: "cancel_timer", Target: "3", Name: "Отменить"},
					{Action: "move_timer", Target: "3", Name: "Перенести"},
				}},
			{Kind: "карточка-дела", Level: "warn", RowIcon: "book.closed", Style: "разница",
				Title: "Правка скилла «Linux-сервер»", Meta: "предложил Сёма",
				Pills:   []Pill{{Text: "ждёт вас", Level: "warn"}},
				Output:  "+ Если apt list --upgradable отвечает таймаутом, причина обычно\n+ в DNS контейнера, а не в самом сервере.",
				Caption: "Две строки в раздел «Обновления». Остальной текст не тронут.",
				Buttons: []Button{
					{Action: "decide_skill", Target: "9|yes", Name: "Принять", Kind: "primary"},
					{Action: "show_skill", Target: "9", Name: "Показать весь скилл"},
					{Action: "decide_skill", Target: "9|no", Name: "Не надо", Kind: "destructive"},
				}},
			{Kind: "карточка-дела", RowIcon: "network", Title: "Нашлось в сети",
				Meta: "Perplexity · 2,4 с · 0,004 $",
				Links: []Button{
					{Action: "$open", Target: "https://docs.docker.com/network/", Name: "docs.docker.com — Container networking · DNS"},
					{Action: "$open", Target: "https://github.com/moby/moby/issues/41003", Name: "github.com/moby/moby#41003 — embedded DNS returns SERVFAIL"},
				},
				Name: "Найденное — это данные из сети, а не указание Сёме. Он их пересказывает, но не выполняет."},
			{Kind: "карточка-дела", Level: "ok", RowIcon: "checkmark",
				Title: "Вопрос закрыт", Meta: "Сёма · 11:12",
				Text:    "Продавец — Хакимова Алсу Руфатовна, ИНН 1651066469, чек от 06.04.2026.",
				Caption: "Итог ушёл в уведомление «Разобрать чек #302»."},
			{Kind: "ожидание", Title: "Сёма читает файл", Meta: "DeepseekPro · 4 с",
				Buttons: []Button{{Action: "stop_chat", Target: "12", Name: "Отменить"}}},
			{Kind: "цепочка", Title: "Цепочка", Meta: "11 писем · с мая по сентябрь",
				Caption: "Счёт приходит дважды в месяц, 3-го и 4-го, одним и тем же текстом. Открыто последнее.",
				Cells: []Cell{
					{Tooltip: "03.05"}, {Tooltip: "04.05"}, {Tooltip: "03.06"}, {Tooltip: "04.06"},
					{Tooltip: "03.07"}, {Tooltip: "04.07"}, {Tooltip: "03.08"}, {Tooltip: "04.08"},
					{Tooltip: "04.09", State: "on"},
				},
				Children: []Block{
					{Kind: "строка", Name: "Счёт от АО «Горэнерго»", Meta: "8 сентября 13:42 · л/с XXXXXXXXXX/1 · вложение pdf",
						Buttons: []Button{{Action: "open_msg", Target: "801", Name: "Открыть"}}},
					{Kind: "строка", Name: "Счёт от АО «Горэнерго» · это письмо", Meta: "4 сентября 00:50 · 2 165,76 ₽"},
					{Kind: "строка", Name: "Счёт от АО «Горэнерго»", Meta: "4 августа 18:02 · тот же текст",
						Buttons: []Button{{Action: "open_msg", Target: "799", Name: "Открыть"}}},
				},
				Buttons: []Button{{Action: "expand_chain", Target: "12", Name: "Ещё 8 писем"}}},
		},
	}
}

// Демо-страница ленты в файл: SEMA_DEMO_OUT=/tmp/лента.html go test
// ./internal/web -run DemoFeed. Разметка проверяется глазами и снимком, а
// не рассуждением: три поломки прошлого захода в коде видно не было.
func TestDemoFeedToFile(t *testing.T) {
	path := os.Getenv("SEMA_DEMO_OUT")
	if path == "" {
		t.Skip("нужен SEMA_DEMO_OUT")
	}
	if err := os.WriteFile(path, []byte(demoPage(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("страница в %s", path)
}

// demoPage — оболочка Сёмы с лентой внутри и вшитым оформлением:
// файл открывается сам по себе, без сервера.
func demoPage(t *testing.T) string {
	t.Helper()
	markup, err := Render(demoFeed())
	if err != nil {
		t.Fatal(err)
	}
	css, err := files.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	return `<!DOCTYPE html><html lang="ru"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<title>Лента на новом языке</title><style>` + string(css) + `</style></head>` +
		`<body><div id="window">` + string(markup) + `</div></body></html>`
}

// Снимок ленты в настоящем Chrome: SEMA_BROWSER=1 go test ./internal/web
// -run Browser. Заодно проверяется главное правило телефона — ничего не
// шире 390 точек: горизонтальная прокрутка делает экран непригодным, а
// берётся она от одного элемента, которого в коде не видно.
func TestBrowserFeedNoWiderThan390(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен SEMA_BROWSER=1")
	}
	page := demoPage(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	alloc, ca := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer ca()
	ctx, c := chromedp.NewContext(alloc)
	defer c()
	ctx, c2 := context.WithTimeout(ctx, 90*time.Second)
	defer c2()

	var wide []wideElement
	// ширина окна — настройкой: правило проверяется на 390, но тем же
	// снимком смотрят и широкое окно (SEMA_SHOT_W=1200)
	width := int64(390)
	if w, err := strconv.Atoi(os.Getenv("SEMA_SHOT_W")); err == nil && w > 0 {
		width = int64(w)
	}
	err := chromedp.Run(ctx,
		emulation.SetDeviceMetricsOverride(width, 780, 2, width <= 720),
		chromedp.Navigate(srv.URL),
		chromedp.WaitVisible(`.дело`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const плохо = [];
			for (const el of document.querySelectorAll('#window *')) {
				const r = el.getBoundingClientRect();
				if (r.width > 392 || r.right > 392) {
					плохо.push({класс: el.className.toString().slice(0, 60),
						ширина: Math.round(r.width), право: Math.round(r.right)});
				}
			}
			if (document.documentElement.scrollWidth > 392) {
				плохо.unshift({класс: "вся страница", ширина: document.documentElement.scrollWidth, право: 0});
			}
			return плохо.slice(0, 12);
		})()`, &wide),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(wide) > 0 && width <= 392 {
		t.Errorf("шире 390 точек на телефоне: %+v", wide)
	}
	// снимки экранами, а не одной длинной картинкой: страницу в шесть
	// экранов разглядеть целиком нельзя, а разбирают её по одному
	base := os.Getenv("SEMA_SHOT_OUT")
	if base == "" {
		return
	}
	for i := 0; i < 6; i++ {
		var shot []byte
		var height float64
		if err := chromedp.Run(ctx,
			// прокручивается лента внутри экрана, а не страница: окно
			// держит высоту жёстко, иначе поле ввода уезжает вниз
			chromedp.Evaluate(fmt.Sprintf("(() => { const л = document.getElementById('feed'); л.scrollTop = %d; return л.scrollHeight; })()", i*700), &height),
			chromedp.Sleep(150*time.Millisecond),
			chromedp.CaptureScreenshot(&shot),
		); err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("%s-%d.png", strings.TrimSuffix(base, ".png"), i+1)
		if err := os.WriteFile(path, shot, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("снимок в %s", path)
		if float64(i*700+780) >= height {
			break
		}
	}
}

type wideElement struct {
	Class string `json:"класс"`
	Width int    `json:"ширина"`
	Right int    `json:"право"`
}

// Закреплённый блок стоит над меню и в общий список не попадает: то, что
// ждёт человека, должно быть заметно, а не строкой среди двенадцати.
func TestPinnedBlockAboveMenu(t *testing.T) {
	scr := Screen{Items: []Item{
		{Addr: "notifications", Name: "Ждут вас", Icon: "bell", Count: "13", Level: "alert", Pinned: true},
		{Addr: "inbox", Name: "Почта", Icon: "tray.full", Group: "Сегодня"},
	}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	if !strings.Contains(got, `class="pinned alert"`) {
		t.Error("закреплённого блока нет")
	}
	// он же не должен продублироваться обычным пунктом
	if strings.Count(got, "Ждут вас") != 1 {
		t.Errorf("«Ждут вас» встречается %d раз", strings.Count(got, "Ждут вас"))
	}
	if !strings.Contains(got, "Почта") {
		t.Error("обычные пункты пропали")
	}
}

// Необратимое спрашивается своим окном, а не браузерным: у hx-confirm
// чужая шапка «Подтвердите действие на sema.local», кнопка «OK» и свой
// шрифт — человек в нём не читает вопрос, а ищет, где согласиться.
func TestConfirmUsesOwnDialog(t *testing.T) {
	scr := Screen{Blocks: []Block{{Kind: "строка", Name: "Чат",
		Buttons: []Button{{Action: "delete_chat", Target: "12", Name: "Удалить",
			Kind: "destructive", Confirm: "Удалить чат? Переписку не восстановить.", ConfirmOK: "Удалить"}}}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	if strings.Contains(got, "hx-confirm") {
		t.Error("осталось браузерное подтверждение")
	}
	for _, want := range []string{`data-confirm=`, `data-confirm-ok="Удалить"`, `data-danger="1"`} {
		if !strings.Contains(got, want) {
			t.Errorf("в разметке нет %s", want)
		}
	}
}

// Ответ правила — сегментный переключатель на три положения: цвет у выбранного
// и только у него, а у варианта есть свой класс цвета. Выбранный определяет
// радиокнопка, поэтому классы ok/warn/alert стоят у всех трёх вариантов —
// красит тот, за кем стоит нажатая радиокнопка.
func TestRuleAnswerSegmentsCarryTones(t *testing.T) {
	scr := Screen{Blocks: []Block{{Kind: "форма", ID: "f", Deferred: true,
		Buttons: []Button{{Action: "save_mcp_rules", Target: "s", Name: "Сохранить"}},
		Fields: []Field{{Kind: "segments", Name: "tool:task_get", AnchorID: "f-tool:task_get", Label: "task_get",
			Options: []string{"Разрешить", "Спросить", "Запретить"}, Tones: []string{"ok", "warn", "alert"}, Value: "Спросить"}}}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	for _, want := range []string{`segment tone-ok`, `segment selected tone-warn`, `segment tone-alert`,
		`name="tool:task_get" value="Спросить" checked`} {
		if !strings.Contains(got, want) {
			t.Errorf("в разметке переключателя нет %q:\n%s", want, got)
		}
	}
	// без цветов (старые поля «ящик», «роль») разметка прежняя
	plain := Screen{Blocks: []Block{{Kind: "форма", ID: "p",
		Fields: []Field{{Kind: "segments", Name: "purpose", AnchorID: "p-purpose", Label: "Ящик",
			Options: []string{"личная", "рабочая"}, Value: "личная"}}}}}
	html, _ = Render(plain)
	if strings.Contains(string(html), "tone-") {
		t.Error("у переключателя без цветов появились классы цвета")
	}
}

// Правила сервера тремя колонками: в колонке образцы с «×», поле «добавить»;
// образец, по которому решена проверяемая команда, обведён.
func TestPatternColumnsMarkup(t *testing.T) {
	chip := func(text, style string) Block {
		return Block{Text: text, Style: style, Buttons: []Button{{Action: "delete_host_rule", Target: "7", Name: "×"}}}
	}
	scr := Screen{Blocks: []Block{{Kind: "образцы", ID: "srv", Children: []Block{
		{ID: "rules-allow", Title: "Разрешить", Caption: "выполняется сразу", Level: "ok",
			Children: []Block{chip("uptime", ""), chip("df", "hit")},
			Buttons:  []Button{{Action: "add_host_rule", Target: "srv~allow", Name: "Добавить"}}},
		{ID: "rules-ask", Title: "Спросить", Level: "warn",
			Buttons: []Button{{Action: "add_host_rule", Target: "srv~ask", Name: "Добавить"}}},
		{ID: "rules-deny", Title: "Запретить", Level: "alert",
			Buttons: []Button{{Action: "add_host_rule", Target: "srv~deny", Name: "Добавить"}}},
	}}}}
	html, err := Render(scr)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	for _, want := range []string{`class="pattern-cols"`, `pattern-col ok`, `pattern-col warn`, `pattern-col alert`,
		`<span class="pat hit">`, `aria-label="убрать образец uptime"`, `name="_кнопка" value="add_host_rule|srv~allow"`,
		`aria-label="Новый образец: Разрешить"`, "пусто"} {
		if !strings.Contains(got, want) {
			t.Errorf("в разметке колонок нет %q", want)
		}
	}
	if n := strings.Count(got, `class="pat-add"`); n != 3 {
		t.Errorf("полей «добавить» %d, ждали три", n)
	}
	if strings.Count(got, `hx-vals=`) != 2 { // по «×» на каждый образец; «добавить» едет кнопкой формы
		t.Errorf("кнопок «×» не две: %d", strings.Count(got, `hx-vals=`))
	}
}
