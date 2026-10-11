package api

import (
	"net/http/httptest"
	"testing"

	"github.com/chromedp/chromedp"
)

// Нажатие клетки не уносит человека в начало списка: порция «Показать ещё»,
// отбор и прокрутка остаются. Раньше то же держали раскрытые строки каналов
// (запись экрана 13 сентября 2026: «постоянно сворачивает список настроек»);
// теперь строки нечего раскрывать, а ту же беду несёт перечитывание экрана — после
// действия оно возвращало первые тридцать строк, и нужной на экране уже не было.
func TestBrowserMatrixCellKeepsPlaceInList(t *testing.T) {
	serversBrowserOnly(t)
	s, st := telegramWithChannels(t, 40)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := serversDesktop(t, srv.URL, tgScreen)
	waitScreen(t, ctx, tgScreen)
	mxPoll(t, ctx, "первая порция", `document.querySelectorAll('tr.matrix-row').length === 30`)

	// «Показать ещё», потом отбор «Каналы»: сорок строк, а не тридцать
	stnClickText(t, ctx, `.filters-bar .segments .segment`, "Каналы")
	// ждём сам отбор, а не число строк: тридцать строк было и до нажатия, и
	// проверка проходила раньше, чем приезжал отобранный список, — «ещё»
	// нажималось на прежнем экране, и сорок каналов не набиралось никогда
	// (2 октября 2026, на загруженной машине через раз)
	mxPoll(t, ctx, "каналы первой порцией", `document.querySelector('.filters-bar .segment.selected').textContent.startsWith('Каналы') && document.querySelectorAll('tr.matrix-row').length === 30`)
	stnClick(t, ctx, `#feed button[data-action="$more"]`)
	mxPoll(t, ctx, "все сорок каналов", `document.querySelectorAll('tr.matrix-row').length === 40`)

	// клетка в последней строке, прокрутка к ней
	const row = `#mx-tg-140`
	cell := row + ` .cell-button[title^="Канал номер 40 · читать посты"]`
	var before float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const л = document.getElementById('feed'); document.querySelector('`+row+`').scrollIntoView({block: 'center'}); return л.scrollTop; })()`, &before)); err != nil {
		t.Fatal(err)
	}
	stnClick(t, ctx, cell)
	mxPoll(t, ctx, "клетка включена, порция, отбор и прокрутка остались",
		`!!document.querySelector('`+cell+`.on') && document.querySelectorAll('tr.matrix-row').length === 40 && document.querySelector('.filters-bar .segment.selected').textContent.startsWith('Каналы')`)
	if c, _ := st.TGChat(telegramAccount, 140); !c.Collect {
		t.Error("право «читать посты» последнему каналу не выдано")
	}
	var after float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('feed').scrollTop`, &after)); err != nil {
		t.Fatal(err)
	}
	if before > 0 && after < before-60 {
		t.Errorf("список уехал наверх: прокрутка была %v, стала %v", before, after)
	}
	if v := mxRead(t, ctx); len(v.More) != 0 {
		t.Errorf("после нажатия вернулась кнопка «ещё»: %v", v.More)
	}
}
