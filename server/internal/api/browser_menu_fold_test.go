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

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Сворачивание групп и контейнеров разговоров — теперь в главном меню, а не
// во второй панели рядом. Три правила, которые держали тесты панели
// (записи экрана 13 сентября 2026), переехали сюда же:
//   - контейнер сворачивается даже когда стоишь внутри него, и решение
//     держится после перерисовки;
//   - группа и контейнер сворачиваются независимо и не перетирают друг
//     другу решение;
//   - заголовок группы — мишень под палец, а не подпись мелким кеглем.
func TestBrowserGroupsAndContainersCollapseInMenu(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Браузер")
	}
	s, st := testServer(t)
	я, _ := st.CreateChat("Telegram · Вадим")
	главный, _ := st.CreateThread(я, store.ChatKindSema, 0, "Сёма · Вадим")
	_ = st.AddChatMsg(главный, "assistant", "готово")
	дом, _ := st.CreateChat("Telegram · Дом")
	_ = st.SetChatKind(дом, store.ChatKindGroup)
	заявки, _ := st.CreateConversation(дом, store.ChatKindTopic, 7, "Заявки")
	_ = st.AddChatMsg(заявки, "peer", "батарея не греет")
	свой, _ := st.CreateChat("Про отпуск")
	_ = st.AddChatMsg(свой, "user", "поедем")

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelTime := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTime()

	addr := func(n int64) string { return "chat:" + strconv.FormatInt(n, 10) }
	узелДома := "menu-" + addr(дом)
	const группа = "menu-чаты:Telegram"

	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1290, 900),
		chromedp.Navigate(srv.URL+"/#screen="+addr(заявки))); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть: %v", err)
	}
	waitScreen(t, ctx, addr(заявки))
	_ = chromedp.Run(ctx, chromedp.Sleep(400*time.Millisecond))

	раскрыт := func(id string) bool {
		var да bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const у = document.getElementById(`+strconv.Quote(id)+`); return !!(у && у.open); })()`, &да)); err != nil {
			t.Fatal(err)
		}
		return да
	}
	нажатьЗаголовок := func(id string) {
		t.Helper()
		if err := chromedp.Run(ctx,
			chromedp.Click(`//details[@id=`+strconv.Quote(id)+`]/summary`, chromedp.BySearch),
			chromedp.Sleep(200*time.Millisecond)); err != nil {
			t.Fatalf("нажать заголовок %s: %v", id, err)
		}
	}
	перерисовать := func() {
		t.Helper()
		// тот же экран заново — так его перечитывают опрос и провод
		if err := chromedp.Run(ctx,
			chromedp.Evaluate(`document.getElementById('screen').dispatchEvent(new Event('sema:refresh')); true`, nil),
			chromedp.Sleep(700*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}

	// дерево без вставок: уровни — отступом, а не плашкой. Карман в кармане
	// давал белую карточку на каждом уровне (16 сентября 2026); белым
	// остаётся только открытый разговор
	var плашки []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const прозрачный = (ц) => ц === 'rgba(0, 0, 0, 0)' || ц === 'transparent';
		return [...document.querySelectorAll('#menu-ask .well')]
			.filter((к) => !прозрачный(getComputedStyle(к).backgroundColor))
			.map((к) => (к.parentElement && к.parentElement.id) + ' → ' + getComputedStyle(к).backgroundColor);
	})()`, &плашки)); err != nil {
		t.Fatal(err)
	}
	if len(плашки) > 0 {
		t.Errorf("в дереве разговоров остались плашки-вставки: %v", плашки)
	}

	// группа и контейнер приходят раскрытыми
	if !раскрыт(группа) || !раскрыт(узелДома) {
		t.Fatalf("группа %v, контейнер %v — должны прийти раскрытыми", раскрыт(группа), раскрыт(узелДома))
	}

	// заголовок группы — мишень, а не подпись мелким кеглем
	var высота float64
	_ = chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('#`+strings.ReplaceAll(группа, ":", `\\:`)+` > summary').getBoundingClientRect().height`, &высота))
	if высота < 28 {
		t.Errorf("заголовок группы высотой %.0f — в него не попасть", высота)
	}

	// контейнер сворачивается, хотя стоим внутри него, и держится после перерисовки
	нажатьЗаголовок(узелДома)
	if раскрыт(узелДома) {
		t.Fatal("контейнер не свернулся, пока внутри открыта его тема")
	}
	перерисовать()
	if раскрыт(узелДома) {
		t.Error("свёрнутый контейнер развернулся после перерисовки экрана")
	}

	// группа и контейнер независимы: свернули и раскрыли группу — контейнер остался свёрнутым
	нажатьЗаголовок(группа)
	if раскрыт(группа) {
		t.Fatal("группа не свернулась")
	}
	нажатьЗаголовок(группа)
	перерисовать()
	if !раскрыт(группа) {
		t.Error("раскрытая заново группа снова свёрнута")
	}
	if раскрыт(узелДома) {
		t.Error("решение группы перетёрло решение контейнера: контейнер развернулся")
	}
}
