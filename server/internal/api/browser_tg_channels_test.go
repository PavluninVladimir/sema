package api

// Каналы Telegram в настоящем Chrome: строки той же матрицы прав, что и чаты, —
// «Показать ещё» под ней, поиск и отбор на сервере, а в правах канала нет «отвечать»,
// «админ» и «напоминать» — прочерки; нажатиями, тем местом, которым пользуется
// человек. До 2 октября 2026 каналы были отдельным ленивым разделом.

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestBrowserTelegramChannelsInMatrix(t *testing.T) {
	serversBrowserOnly(t)
	s, _ := telegramWithChannels(t, 40) // человек, группа и сорок каналов: сорок два чата
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := serversDesktop(t, srv.URL, tgScreen)
	waitScreen(t, ctx, tgScreen)

	// ленивых разделов нет: матрица приезжает сразу, первой порцией в тридцать строк
	mxPoll(t, ctx, "первая порция", `document.querySelectorAll('tr.matrix-row').length === 30`)
	v := mxRead(t, ctx)
	if v.Fragments != 0 {
		t.Fatalf("на экране остались ленивые разделы: %d", v.Fragments)
	}
	if len(v.More) != 1 || !strings.Contains(v.More[0], "30 из 42") {
		t.Fatalf("«Показать ещё»: %v", v.More)
	}

	// права канала: читать и файлы есть, отвечать, админ и напоминать — прочерки
	var channel mxRow
	for _, r := range v.Rows {
		if strings.Contains(r.Who, "Канал номер") {
			channel = r
			break
		}
	}
	// четыре прочерка: отвечать, писать первым (публиковать нельзя), админ, напоминать
	if channel.ID == "" || channel.NA != 4 || channel.On != 0 {
		t.Fatalf("строка канала: %+v", channel)
	}
	if strings.Join(channel.Chips, "|") != "Читать|Файлы|Не трогать" {
		t.Errorf("чипы канала подписчика: %v", channel.Chips)
	}

	// «ещё» — настоящее нажатие; экран просит порцию с удвоенным пределом
	stnClick(t, ctx, `#feed button[data-action="$more"]`)
	mxPoll(t, ctx, "все сорок два", `document.querySelectorAll('tr.matrix-row').length === 42`)
	if v = mxRead(t, ctx); len(v.More) != 0 {
		t.Errorf("кнопка «ещё» осталась, когда показано всё: %v", v.More)
	}

	// поиск — на сервере, по нику: канала с таким ником нет среди тридцати первых
	stnType(t, ctx, "#search", "ozon_news")
	mxPoll(t, ctx, "найден канал по нику", `document.querySelectorAll('tr.matrix-row').length === 1 && !!document.getElementById('mx-tg-101')`)
	if v = mxRead(t, ctx); len(v.Rows) != 1 || !strings.Contains(v.Rows[0].Who, "Новости Ozon") {
		t.Errorf("по нику нашлось %+v", v.Rows)
	}
	// отбор «Каналы» держит поиск, а «Личные» по тому же нику ничего не находит
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const q = document.getElementById('search'); q.value = '';
	  q.dispatchEvent(new Event('input', {bubbles: true})); return true; })()`, nil)); err != nil {
		t.Fatal(err)
	}
	mxPoll(t, ctx, "поиск стёрт", `document.querySelectorAll('tr.matrix-row').length >= 30`)
	stnClickText(t, ctx, `.filters-bar .segments .segment`, "Каналы")
	mxPoll(t, ctx, "только каналы, первой порцией", `document.querySelectorAll('tr.matrix-row').length === 30 && ![...document.querySelectorAll('tr.matrix-row')].some((r) => r.textContent.includes('Митя'))`)
}
