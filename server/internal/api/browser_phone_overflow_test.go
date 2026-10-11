package api

import (
	"context"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Телефон: страница никогда не едет вбок, а нижняя полоса стоит ровно.
//
// Записано со снимков владельца на iPhone (1 октября 2026, ширина 390):
// на экране разговора страница уехала вбок — заголовок прижат к левому
// краю без отступа, у карточек обрезано начало, справа пустое место, — а
// пункты нижней полосы стояли не по центру своих колонок. Корень один:
// какой-то элемент ленты шире колонки, и браузер телефона раздвигает
// страницу под него, а полоса растягивается вместе со страницей.
//
// Меряем в Chrome с настоящей эмуляцией телефона (ширина 390, высота 844,
// `mobile`, касания): без `mobile` Chrome не раздвигает окно под
// выпирающее, и беда выглядела бы здоровой. Ждём условий на странице, а
// не сроков.

// phoneSide — минимальный боковой отступ у заголовка и ленты, CSS-точек.
const phoneSide = 16

// phoneChrome поднимает Chrome с эмуляцией iPhone и закрывает его вместе
// с тестом: живым Chrome после себя не оставляем.
func phoneChrome(t *testing.T, base string, timeout time.Duration) context.Context {
	t.Helper()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", "new"))...)
	t.Cleanup(cancelAlloc)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(cancel)
	ctx, cancelTime := context.WithTimeout(ctx, timeout)
	t.Cleanup(cancelTime)

	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			if err := emulation.SetDeviceMetricsOverride(390, 844, 3, true).Do(ctx); err != nil {
				return err
			}
			return emulation.SetTouchEmulationEnabled(true).Do(ctx)
		}),
		chromedp.Navigate(base+"/")); err != nil {
		if strings.Contains(err.Error(), "executable") || strings.Contains(err.Error(), "not found") {
			t.Skipf("Chrome не найден: %v", err)
		}
		t.Fatalf("открыть страницу: %v", err)
	}
	return ctx
}

// phoneFixtures — данные, на которых телефон ломался: разговор с карточками
// записей и длинными строками, письма с длинной темой и адресом, правила
// MCP, правила сервера. Возвращает адреса экранов по именам.
func phoneFixtures(t *testing.T) (*Server, map[string]string) {
	t.Helper()
	s, st := rulesMCPFix(t)
	экраны := map[string]string{}

	// разговор «Сёма · Вадим» с записями: ночные смены, строка без
	// пробелов (ссылка), длинная команда
	я, err := st.CreateChat("Telegram · Вадим")
	if err != nil {
		t.Fatal(err)
	}
	сёма, err := st.CreateThread(я, store.ChatKindSema, 0, "Сёма · Вадим")
	if err != nil {
		t.Fatal(err)
	}
	_ = st.AddChatMsg(сёма, "assistant", "Готово. Смотрите https://sema.example.org/очень/длинная/ссылка/без/единого/пробела/которая/не/переносится/сама?x=1&y=2&z=3")
	_ = st.AddChatMsg(сёма, "tool", `{"title":"Ночная смена · Microtik","result":"сделано 2, ждёт вас 1","code":"sudo /usr/bin/apt-get -o Dpkg::Options::=--force-confold -o Dpkg::Options::=--force-confdef -y upgrade && systemctl restart очень-длинное-имя-службы-без-пробелов.service"}`)
	// карточки разных видов с длинными строками без пробелов
	длинное := "очень-длинное-слово-без-единого-пробела-которое-не-переносится-само-по-себе-никак-совсем"
	for _, карточка := range []string{
		`{"title":"Команда на сервере · ` + длинное + `","ssh":{"host":"` + длинное + `","cmd":"sudo /usr/bin/apt-get -o Dpkg::Options::=--force-confold -y upgrade && systemctl restart ` + длинное + `.service","status":"pending","pending_id":1}}`,
		`{"title":"Ответ в Telegram","tg":{"who":"` + длинное + `","text":"` + длинное + ` ` + длинное + `","status":"pending","id":1}}`,
		`{"title":"Запомнить · ` + длинное + `","memory":{"area":"` + длинное + `","kind":"fact","status":"saved","note":"` + длинное + `"}}`,
		`{"title":"Скилл · ` + длинное + `","skill":{"name":"` + длинное + `","title":"` + длинное + `","new":true,"diff":"+ ` + длинное + `\n- ` + длинное + `","status":"proposed","id":"x"}}`,
		`{"title":"Таймер · ждёт","timer":{"at":"2026-10-02T10:00:00Z","note":"` + длинное + `","status":"set"}}`,
		`{"title":"Вопрос закрыт","done":{"summary":"` + длинное + `"}}`,
		`{"title":"MCP · ` + длинное + `","mcp":{"server":"` + длинное + `","tool":"` + длинное + `","args":"{\"` + длинное + `\":1}","status":"pending","pending_id":1}}`,
	} {
		if err := st.AddChatMsg(сёма, "tool", карточка); err != nil {
			t.Fatal(err)
		}
	}
	// файлы с длинными именами: чипы вложений не переносятся сами
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")
	имяФайла := "Скриншот-экрана-2026-10-01-в-02.03.11-очень-длинное-имя-файла-без-пробелов-совсем.png"
	if att, _, err := s.eng.AddFile(context.Background(), имяФайла, "image/png", png,
		engine.FileSource{Kind: store.SourceTG, Ref: "проба"}, nil); err == nil && att != nil {
		_ = st.AddChatMsg(сёма, "user", fmt.Sprintf("Прикрепил файл «%s» (#%d)", имяФайла, att.ID))
	}
	if att, _, err := s.eng.AddFile(context.Background(), "Договор-аренды-помещения-№-12345-от-01-октября-2026-года-подписанный-обеими-сторонами.pdf",
		"application/pdf", []byte("%PDF-1.4 проба"), engine.FileSource{Kind: "upload"}, nil); err == nil && att != nil {
		_ = st.AddChatMsg(сёма, "user", fmt.Sprintf("Прикрепил файл «%s» (#%d)", att.Filename, att.ID))
	}
	_ = st.AddChatMsg(сёма, "user", длинное+" "+длинное)
	_ = st.AddChatMsg(сёма, "assistant", "```\n"+длинное+длинное+"\n```\n| a | b |\n|---|---|\n| "+длинное+" | "+длинное+" |")
	for _, n := range []*store.Notification{
		{Key: "night:Microtik", Source: "core", Level: "warn", State: store.NotifyOpen,
			Title:   "Microtik · ночная смена: сделано 2, ждёт вас 1, отложено 3",
			Summary: "сделано 2, ждёт вас 1, не делали 0, отложено 3",
			Fields: []store.NotifyField{{Label: "Сервер", Value: "Microtik", Level: "ok"},
				{Label: "Журнал", Value: "https://sema.example.org/settings/servers/log/Microtik/очень/длинный/адрес/без/пробелов"}},
			Actions: []store.NotifyAction{
				{Label: "Открыть журнал", Kind: "goto", Screen: "settings:servers~log:Microtik", Style: "primary"}}},
		{Key: "fp:srv-vpn", Source: "core", Level: "alert", State: store.NotifyOpen,
			Title: "Сменился отпечаток сервера srv-vpn: SHA256:abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ",
			Actions: []store.NotifyAction{
				{Label: "Принять новый", Kind: "core", Op: "accept_fp", Ref: "srv-vpn", Style: "primary"},
				{Label: "Отклонить", Kind: "core", Op: "reject_fp", Ref: "srv-vpn"}}},
		{Key: "night:srv-vpn", Source: "core", Level: "ok", State: store.NotifyOpen,
			Title: "srv-vpn · ночная смена", Summary: "сделано 1, отложено 1",
			Fields: []store.NotifyField{{Label: "Осмотр", Value: "читающих команд: 12 · шагов в наряде: 2"},
				{Label: "Сделано", Value: "apt-get -o Dpkg::Options::=--force-confold -y upgrade — очень-длинное-имя-пакета-без-пробелов-и-переносов-совсем-никаких-версия-1.2.3.4.5.6.7.8.9"}},
			Actions: []store.NotifyAction{
				{Label: "Открыть журнал", Kind: "goto", Screen: "settings:servers~log:srv-vpn", Style: "primary"},
				{Label: "Понял", Kind: "dismiss"}}},
		{Key: "night:WireGuard", Source: "core", Level: "ok", State: store.NotifyOpen,
			Title: "WireGuard · ночная смена", Summary: "сделано 0",
			Actions: []store.NotifyAction{
				{Label: "Открыть журнал", Kind: "goto", Screen: "settings:servers~log:WireGuard", Style: "primary"},
				{Label: "Понял", Kind: "dismiss"}}},
		{Key: "budget:module:chek", Source: "module:очень-длинное-имя-модуля-чеков", Level: "info", State: store.NotifyOpen,
			Title: "Исчерпан бюджет «Технический форум»"},
	} {
		if _, err := st.PutNotification(n); err != nil {
			t.Fatal(err)
		}
	}
	экраны["разговор"] = "chat:" + itoa(сёма)

	// канал Telegram: пост с картинкой и длинной ссылкой
	if err := st.SaveTGAccount(&store.TGAccount{Phone: telegramAccount, State: "ok", Enabled: true, Every: 1, Batch: 50}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTGChat(&store.TGChat{Account: telegramAccount, ID: 2, Title: "Технический форум", Kind: "channel", Collect: true, CanPost: true}); err == nil {
		if канал, _ := s.eng.TGOpenChat(context.Background(), telegramAccount, 2); канал != 0 {
			if att, _, err := s.eng.AddFile(context.Background(), "photo-1.jpg", "image/png", png,
				engine.FileSource{Kind: store.SourceTG, Ref: "проба"}, nil); err == nil && att != nil {
				_ = st.AddChatMsgFrom(канал, "peer", "", fmt.Sprintf("Свежее исследование "+длинное+"\n\nhttps://arxiv.org/abs/2610.12345v1/очень/длинная/ссылка/без/пробелов/и/переносов «photo-1.jpg» (#%d)", att.ID))
			}
			экраны["канал"] = "chat:" + itoa(канал)
		}
	}
	экраны["уведомления"] = notifyScreen

	// письма: длинная тема без пробелов и длинный адрес отправителя
	var письмо int64
	for i, m := range []*store.Message{
		{MessageID: "<a@т>", Subject: "Счёт-фактура-№12345-от-01-октября-2026-года-за-услуги-связи-и-хостинга-ООО-Ромашка",
			FromAddr: "very.long.sender.address.without.breaks.at.all@subdomain.example-company-name.example.рф",
			FromName: "Очень Длинное Имя Отправителя Которое Не Помещается", Status: store.StatusInboxOK},
		{MessageID: "<b@т>", Subject: "Короткая тема", FromAddr: "a@б.рф", FromName: "А", Status: store.StatusInboxOK},
	} {
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			письмо = m.ID
			_ = st.SaveBody(m.ID, store.Body{Text: "К оплате 512,30 ₽\nhttps://example.com/очень/длинная/ссылка/без/пробелов/и/переносов/совсем/никаких/вообще/нигде"})
		}
	}
	// «Похоже на спам — подтвердите»: группы отправителей с длинными адресами
	// и строки писем внутри «Подробнее». 4 октября 2026 владелец прислал
	// снимок: на iPhone строки этого раздела были обрезаны слева, кнопки
	// «Не спам» висели посередине экрана, а справа было пусто
	for i, m := range []*store.Message{
		{MessageID: "<sp1@т>", Subject: "Вас ждёт подарок от магазина — откройте до конца недели",
			FromAddr: "ts+acct_1pgmnepqwertyuiopasdfgh@stripe-notifications.example.com", FromName: "Stripe",
			Confidence: 0.92, Reason: "правило: спамер по адресу — письма этого отправителя уходят в спам"},
		{MessageID: "<sp2@т>", Subject: "Ваш букет» и ещё 1 тема · правитель (перенос не выполнен)",
			FromAddr: "ts+acct_1pgmnepqwertyuiopasdfgh@stripe-notifications.example.com", FromName: "Stripe",
			Confidence: 0.9, Reason: "правило: спамер по адресу"},
		// одиночные письма — строки с адресом, временем и ящиком в одной
		// строке мета: именно она раздвигала ленту
		{MessageID: "<sp3@т>", Subject: "Системы для аккаунта",
			FromAddr: "no-reply-accounts-security-notifications@accounts.google.com", FromName: "Google",
			Confidence: 0.6, Reason: "похоже на рассылку"},
		{MessageID: "<sp4@т>", Subject: "Welcome to TypeSafe",
			FromAddr: "ts+acct_1pgmnepqwertyuiopasdfghjklzxcvbnm@typesafe-mailer.example.com", FromName: "TypeSafe",
			Confidence: 0.92, Reason: "похоже на рассылку"},
	} {
		m.Status = store.StatusPending
		m.Account = "owner@example.org"
		m.Date = time.Date(2026, 10, 4, 9, 50+i, 0, 0, time.Local)
		if err := st.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	экраны["почта"] = "inbox"
	экраны["письмо"] = "msg:" + itoa(письмо)

	// серверы: правила с длинными образцами
	if err := st.SaveHost(&store.Host{Name: "очень-длинное-имя-сервера-без-пробелов", Addr: "10.0.0.7", Port: 22, User: "root"}); err != nil {
		t.Fatal(err)
	}
	_ = st.AddHostRule("очень-длинное-имя-сервера-без-пробелов", "allow", "journalctl -u очень-длинное-имя-службы-без-пробелов.service --since today --no-pager")
	_ = st.AddHostRule("очень-длинное-имя-сервера-без-пробелов", "ask", "systemctl restart очень-длинное-имя-службы-без-пробелов.service")
	_ = st.AddHostRule("очень-длинное-имя-сервера-без-пробелов", "deny", "rm -rf /var/lib/очень/длинный/путь/без/пробелов/и/переносов/совсем/никаких")
	экраны["серверы"] = serversScreen
	экраны["серверы-правила"] = rulesAddr("очень-длинное-имя-сервера-без-пробелов", "")

	// инструменты с длинными именами и описаниями: у каждого три положения
	// «Разрешить / Спросить / Запретить», и в 390 точек они не должны
	// раздвигать ни форму, ни страницу
	tools := []string{"task_list", "task_get", "task_create", "task_update", "task_group_list", "project_list",
		"checklist_item_check", "task_brand_new", "whoami",
		"kanban_status_update_for_very_long_tool_name_without_any_breaks_at_all",
		"time_stat_create_with_an_even_longer_name_that_cannot_possibly_fit_in_a_phone_column_row"}
	info := make([]store.MCPTool, 0, len(tools))
	for _, n := range tools {
		info = append(info, store.MCPTool{Name: n, Desc: "описание " + n + " с длинным текстом, который идёт дальше и дальше без остановки"})
	}
	if err := st.SaveMCPServer(&store.MCPServer{Name: "sing", URL: "http://127.0.0.1:1/mcp", Enabled: true,
		Tools: tools, ToolInfo: info}); err != nil {
		t.Fatal(err)
	}
	экраны["интеграции"] = mcpScreen
	экраны["интеграции-сервер"] = entityAddr(mcpScreen, "sing", "")
	экраны["интеграции-правила"] = entityAddr(mcpScreen, "sing", mcpTabRules)
	экраны["интеграции-журнал"] = entityAddr(mcpScreen, "sing", mcpTabLog)
	экраны["интеграции-добавить"] = entityAddr(mcpScreen, mcpNew, "")
	// задача, скрипт и запись памяти с длинными словами: названия приходят
	// от человека и от модели, и перенос им нужен так же, как всем
	_ = st.CreateTask(&store.Task{Title: "Проверка-" + длинное + "-" + длинное, Prompt: длинное + " " + длинное,
		Kind: "prompt", Schedule: "каждые 5 мин", Enabled: true})
	_, _ = st.CreateScript("Скрипт-"+длинное+"-"+длинное, "return 1;")
	_, _ = st.SaveMemory(&store.Memory{Kind: "fact", Area: длинное, Text: длинное + " " + длинное + " " + длинное,
		Status: "active", Agent: длинное})
	экраны["здоровье"] = "health"
	экраны["файлы"] = "files"
	экраны["настройки"] = "settings"
	экраны["задачи"] = "tasks"
	return s, экраны
}

// phoneProbeJS — что выпирает за ширину окна: сама прокрутка, элементы,
// чей край за границей (кроме тех, что прокручиваются или режутся внутри
// своего контейнера), левые границы заголовка и первой карточки.
const phoneProbeJS = `(() => {
  const d = document.documentElement;
  const cw = d.clientWidth;
  // #window режет вбок сам (overflow: hidden), но программно его всё
  // равно можно сдвинуть — фокусом поля, прокруткой к элементу, — поэтому
  // он не считается «внутренним контейнером»: выпирающее за него и есть беда
  const окно = document.getElementById('window');
  // лента — тоже корень: у неё overflow-y: auto, а значит и overflow-x
  // считается auto, и всё, что шире колонки, прокручивает саму ленту вбок
  const лента = document.getElementById('feed');
  const скрыт = (э) => {
    for (let р = э.parentElement; р && р !== лента && р !== окно && р !== document.body && р !== d; р = р.parentElement) {
      const о = getComputedStyle(р).overflowX;
      if (о !== 'visible') return true;
    }
    return false;
  };
  const имя = (э) => э.tagName.toLowerCase() + (э.id ? '#' + э.id : '') +
    (typeof э.className === 'string' && э.className.trim() ? '.' + э.className.trim().split(/\s+/).join('.') : '');
  const плохие = [];
  // в закрытом <details> Safari содержимое не раскладывает вовсе, а Chrome
  // раскладывает: оно не в счёт, кроме самой <summary>
  const вЗакрытом = (э) => {
    for (let р = э; р && р !== document.body; р = р.parentElement) {
      if (р.tagName === 'DETAILS' && !р.open) {
        const с = р.querySelector(':scope > summary');
        if (!(с && с.contains(э))) return true;
      }
    }
    return false;
  };
  for (const э of document.querySelectorAll('#window *')) {
    const р = э.getBoundingClientRect();
    if (р.width === 0 && р.height === 0) continue;
    if (getComputedStyle(э).position === 'fixed') continue;
    if (вЗакрытом(э)) continue;
    // край того, что занимает элемент: его коробка и текст, вылезший из
    // коробки (у переполнения visible это видно только по scrollWidth)
    const видимое = getComputedStyle(э).overflowX === 'visible';
    const правый = Math.max(р.right, видимое && э.clientWidth > 0 ? р.left + э.scrollWidth : 0);
    if (правый > cw + 0.5 || р.left < -0.5) {
      if (скрыт(э)) continue;
      плохие.push({э, имя: имя(э), left: Math.round(р.left), right: Math.round(правый), text: (э.textContent || '').trim().slice(0, 40)});
    }
  }
  // глубочайшие: те, у кого нет выпирающего потомка, — они и есть причина
  const причины = плохие.filter((п) => !плохие.some((q) => q !== п && п.э.contains(q.э)));
  const h1 = document.querySelector('#screen .header h1');
  const первая = (() => {
    const к = document.querySelector('#feed .column') || document.querySelector('#feed');
    if (!к) return null;
    for (const э of к.children) {
      const р = э.getBoundingClientRect();
      if (р.width > 0 && р.height > 0) return {имя: имя(э), left: р.left};
    }
    return null;
  })();
  // окно и лента: у кого содержимое шире самого элемента — называем, чтобы
  // не гадать, откуда взялась ширина
  const скроллеры = [];
  for (const э of [окно, лента]) {
    if (э && э.scrollWidth > э.clientWidth + 1) скроллеры.push(имя(э) + ' ' + э.clientWidth + '→' + э.scrollWidth + ' (прокручено на ' + э.scrollLeft + ')');
  }
  return {
    скроллеры: скроллеры.slice(0, 12),
    sw: Math.max(d.scrollWidth, окно ? окно.scrollWidth : 0, лента ? лента.scrollWidth : 0), cw: cw, bodyW: document.body.scrollWidth,
    причины: причины.slice(0, 8).map((п) => ({имя: п.имя, left: п.left, right: п.right, text: п.text})),
    всего: плохие.length,
    h1: h1 ? h1.getBoundingClientRect().left : -1,
    первая: первая,
  };
})()`

type phoneProbe struct {
	SW      int `json:"sw"`
	CW      int `json:"cw"`
	BodyW   int `json:"bodyW"`
	Причины []struct {
		Имя   string `json:"имя"`
		Left  int    `json:"left"`
		Right int    `json:"right"`
		Text  string `json:"text"`
	} `json:"причины"`
	Скроллеры []string `json:"скроллеры"`
	Всего     int      `json:"всего"`
	H1        float64  `json:"h1"`
	Первая    *struct {
		Имя  string  `json:"имя"`
		Left float64 `json:"left"`
	} `json:"первая"`
}

func (p phoneProbe) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "scrollWidth %d при clientWidth %d (body %d), выпирает %d", p.SW, p.CW, p.BodyW, p.Всего)
	for _, п := range p.Причины {
		fmt.Fprintf(&b, "\n    %s [%d…%d] %q", п.Имя, п.Left, п.Right, п.Text)
	}
	for _, с := range p.Скроллеры {
		fmt.Fprintf(&b, "\n    прокрутка: %s", с)
	}
	return b.String()
}

// TestBrowserPhoneNoSideScroll — на каждом экране из набора страница не
// шире окна, а заголовок и первая карточка отступают от края не меньше
// шестнадцати точек.
func TestBrowserPhoneNoSideScroll(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s, экраны := phoneFixtures(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 4*time.Minute)

	порядок := []string{"разговор", "канал", "почта", "письмо", "уведомления", "интеграции",
		"серверы", "серверы-правила", "здоровье", "файлы", "настройки", "задачи"}
	for _, имя := range порядок {
		адрес := экраны[имя]
		t.Run(имя, func(t *testing.T) {
			if err := chromedp.Run(ctx, chromedp.Evaluate(
				`location.hash = '#screen=`+адрес+`'; true`, nil)); err != nil {
				t.Fatal(err)
			}
			waitScreen(t, ctx, адрес)
			// ждём, пока лента нарисована и экран не перерисовывается
			var готово bool
			if err := chromedp.Run(ctx, chromedp.Poll(
				`!!document.querySelector('#feed') && !document.querySelector('.htmx-request') && document.fonts.status === 'loaded'`,
				&готово, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !готово {
				t.Fatalf("лента не нарисовалась: %v", err)
			}
			// всё раскрыто: свёрнутое тоже когда-нибудь раскроют
			if err := chromedp.Run(ctx, chromedp.Evaluate(
				`document.querySelectorAll('#feed details').forEach((д) => { д.open = true; }); true`, nil)); err != nil {
				t.Fatal(err)
			}
			var p phoneProbe
			if err := chromedp.Run(ctx, chromedp.Evaluate(phoneProbeJS, &p)); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: %s", имя, p)
			if p.SW > p.CW {
				t.Errorf("страница едет вбок: %s", p)
			}
			if p.H1 >= 0 && p.H1 < phoneSide-0.5 {
				t.Errorf("заголовок прижат к краю: левая граница %.1f, нужно не меньше %d", p.H1, phoneSide)
			}
			if p.Первая != nil && p.Первая.Left < phoneSide-0.5 {
				t.Errorf("первая карточка ленты (%s) прижата к краю: %.1f, нужно не меньше %d",
					p.Первая.Имя, p.Первая.Left, phoneSide)
			}
		})
	}
}

// TestBrowserPhoneTabbarCentered — у нижней полосы каждый пункт стоит по
// центру своей колонки: значок над подписью, счётчик прикреплён к углу
// значка и не двигает ни значок, ни подпись.
func TestBrowserPhoneTabbarCentered(t *testing.T) {
	if os.Getenv("SEMA_BROWSER") == "" {
		t.Skip("нужен Chrome: запуск SEMA_BROWSER=1 go test ./internal/api -run Browser -p 1")
	}
	s, экраны := phoneFixtures(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx := phoneChrome(t, srv.URL, 90*time.Second)

	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`location.hash = '#screen=`+экраны["разговор"]+`'; true`, nil)); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, ctx, экраны["разговор"])
	var есть bool
	if err := chromedp.Run(ctx, chromedp.Poll(`document.querySelectorAll('#tabbar .tab').length >= 2`,
		&есть, chromedp.WithPollingTimeout(20*time.Second))); err != nil || !есть {
		t.Fatalf("полосы нет: %v", err)
	}

	// колонка пункта считается по самой полосе (левый край + номер × ширина
	// колонки), а не по коробке кнопки: кнопка, которая не растянулась на
	// колонку, иначе «сама» оказывалась бы по центру своей же коробки
	const мерить = `(() => {
	  const п = document.getElementById('tabbar');
	  const пр = п.getBoundingClientRect();
	  const вкл = Array.from(п.querySelectorAll('.tab'));
	  const колонки = getComputedStyle(п).gridTemplateColumns.split(' ').map(parseFloat);
	  const шаг = пр.width / вкл.length;
	  return {
	    ширина: пр.width, колонки: колонки,
	    вкладки: вкл.map((в, i) => {
	      const р = в.getBoundingClientRect();
	      const з = в.querySelector('.tab-icon'); const н = в.querySelector('.tab-name');
	      const зр = з ? з.getBoundingClientRect() : null; const нр = н ? н.getBoundingClientRect() : null;
	      const ц = в.querySelector('.tab-count, .tab-dot'); const цр = ц ? ц.getBoundingClientRect() : null;
	      return {
	        имя: н ? н.textContent.trim() : '', колонка: пр.left + шаг * (i + 0.5),
	        значок: зр ? зр.left + зр.width / 2 : null, подпись: нр ? нр.left + нр.width / 2 : null,
	        значокВерх: зр ? зр.top : null, подписьВерх: нр ? нр.top : null,
	        счётчикЛево: цр ? цр.left : null, счётчикВерх: цр ? цр.top : null,
	        значокПраво: зр ? зр.right : null, значокНиз: зр ? зр.bottom : null,
	        есть: !!ц, ширинаВкладки: р.width, центрКнопки: р.left + р.width / 2,
	      };
	    }),
	  };
	})()`
	type вкладка struct {
		Имя          string   `json:"имя"`
		Колонка      float64  `json:"колонка"`
		Значок       *float64 `json:"значок"`
		Подпись      *float64 `json:"подпись"`
		ЗначокВерх   *float64 `json:"значокВерх"`
		ПодписьВерх  *float64 `json:"подписьВерх"`
		СчётчикЛево  *float64 `json:"счётчикЛево"`
		СчётчикВерх  *float64 `json:"счётчикВерх"`
		ЗначокПраво  *float64 `json:"значокПраво"`
		ЗначокНиз    *float64 `json:"значокНиз"`
		Есть         bool     `json:"есть"`
		ШиринаВкладк float64  `json:"ширинаВкладки"`
		ЦентрКнопки  float64  `json:"центрКнопки"`
	}
	type полоса struct {
		Ширина  float64   `json:"ширина"`
		Колонки []float64 `json:"колонки"`
		Вкладки []вкладка `json:"вкладки"`
	}
	var без полоса
	if err := chromedp.Run(ctx, chromedp.Evaluate(мерить, &без)); err != nil {
		t.Fatal(err)
	}
	if без.Ширина > 391 {
		t.Errorf("полоса шире окна: %.0f", без.Ширина)
	}
	// у каждого пункта три знака — счётчик не должен двигать ни значок,
	// ни подпись, поэтому второй замер делаем с трёхзначным счётчиком у всех
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
	  document.querySelectorAll('#tabbar .tab').forEach((в) => {
	    let с = в.querySelector('.tab-count');
	    if (!с) { с = document.createElement('span'); с.className = 'tab-count'; (в.querySelector('.tab-glyph') || в).appendChild(с); }
	    с.textContent = '123';
	  });
	  return true;
	})()`, nil)); err != nil {
		t.Fatal(err)
	}
	var с полоса
	if err := chromedp.Run(ctx, chromedp.Evaluate(мерить, &с)); err != nil {
		t.Fatal(err)
	}

	for _, замер := range []struct {
		название string
		п        полоса
	}{{"как есть", без}, {"со счётчиком «123» у всех", с}} {
		for _, в := range замер.п.Вкладки {
			if math.Abs(в.ЦентрКнопки-в.Колонка) > 2 || math.Abs(в.ШиринаВкладк-замер.п.Ширина/float64(len(замер.п.Вкладки))) > 2 {
				t.Errorf("%s: кнопка «%s» не растянута на свою колонку: центр %.1f против %.1f, ширина %.1f",
					замер.название, в.Имя, в.ЦентрКнопки, в.Колонка, в.ШиринаВкладк)
			}
			if в.Значок != nil && math.Abs(*в.Значок-в.Колонка) > 2 {
				t.Errorf("%s: значок «%s» не по центру колонки: %.1f против %.1f", замер.название, в.Имя, *в.Значок, в.Колонка)
			}
			if в.Подпись != nil && math.Abs(*в.Подпись-в.Колонка) > 2 {
				t.Errorf("%s: подпись «%s» не по центру колонки: %.1f против %.1f", замер.название, в.Имя, *в.Подпись, в.Колонка)
			}
			if в.ЗначокВерх != nil && в.ПодписьВерх != nil && *в.ЗначокВерх >= *в.ПодписьВерх {
				t.Errorf("%s: значок «%s» не над подписью", замер.название, в.Имя)
			}
		}
		// колонки равные
		for _, к := range замер.п.Колонки {
			if len(замер.п.Колонки) > 0 && math.Abs(к-замер.п.Колонки[0]) > 1 {
				t.Errorf("%s: колонки неравные: %v", замер.название, замер.п.Колонки)
				break
			}
		}
	}
	// счётчик не двигает значок и подпись: те же центры и те же верхи до и после
	for i := range без.Вкладки {
		a, b := без.Вкладки[i], с.Вкладки[i]
		if a.Значок != nil && b.Значок != nil && math.Abs(*a.Значок-*b.Значок) > 0.5 {
			t.Errorf("счётчик сдвинул значок «%s»: %.1f → %.1f", a.Имя, *a.Значок, *b.Значок)
		}
		if a.Подпись != nil && b.Подпись != nil && math.Abs(*a.Подпись-*b.Подпись) > 0.5 {
			t.Errorf("счётчик сдвинул подпись «%s»: %.1f → %.1f", a.Имя, *a.Подпись, *b.Подпись)
		}
		if a.ЗначокВерх != nil && b.ЗначокВерх != nil && math.Abs(*a.ЗначокВерх-*b.ЗначокВерх) > 0.5 {
			t.Errorf("счётчик сдвинул значок «%s» по вертикали", a.Имя)
		}
		// счётчик прикреплён к правому верхнему углу значка
		if b.СчётчикЛево != nil && b.ЗначокПраво != nil && b.СчётчикВерх != nil && b.ЗначокНиз != nil {
			if *b.СчётчикЛево < *b.ЗначокПраво-8 || *b.СчётчикЛево > *b.ЗначокПраво+8 {
				t.Errorf("счётчик «%s» не у правого края значка: левый край %.1f, правый край значка %.1f",
					a.Имя, *b.СчётчикЛево, *b.ЗначокПраво)
			}
			if *b.СчётчикВерх > *b.ЗначокВерх+4 {
				t.Errorf("счётчик «%s» не у верха значка", a.Имя)
			}
		}
	}
	t.Logf("полоса: ширина %.0f, колонки %v", без.Ширина, без.Колонки)
	for _, в := range без.Вкладки {
		t.Logf("  %-8s колонка %.1f значок %v подпись %v", в.Имя, в.Колонка, в.Значок != nil && *в.Значок != 0, в.Подпись != nil)
	}
}
