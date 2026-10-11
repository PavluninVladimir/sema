package api

// Пуши на телефон: подписка с устройства, имя из строки браузера, строки
// устройств на «Настройки → Уведомления» и действия над ними.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

const (
	iphoneHomeUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148"
	androidUA    = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36"
	macChromeUA  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	macSafariUA  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15"
)

// subscribeAs — подписка так, как её присылает страница: строка браузера в
// заголовке, режим окна и касания в теле.
func subscribeAs(t *testing.T, s *Server, ua, endpoint string, standalone bool, touch int) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"endpoint": endpoint, "standalone": standalone, "touch": touch,
		"keys": map[string]string{"p256dh": "BKey", "auth": "Aut"}})
	req := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(string(body)))
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handlePushSubscribe(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("подписка %s: %d %s", endpoint, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Подписка называет устройство по строке браузера, а не по тому, что
// написала страница; ключ и «знает ли сервер это устройство» берутся одним
// и тем же маршрутом.
func TestPushSubscribeNamesAndStatus(t *testing.T) {
	s, st := testServer(t)
	cases := []struct {
		ua, endpoint string
		standalone   bool
		touch        int
		want         string
	}{
		{iphoneHomeUA, "https://web.push.apple.com/Q1", true, 5, "iPhone · Сёма с экрана «Домой»"},
		{androidUA, "https://fcm.googleapis.com/fcm/send/A1", false, 5, "Chrome на Android"},
		{macChromeUA, "https://fcm.googleapis.com/fcm/send/M1", false, 0, "Chrome на маке"},
		{macSafariUA, "https://web.push.apple.com/S1", false, 0, "Safari на маке"},
	}
	for _, c := range cases {
		out := subscribeAs(t, s, c.ua, c.endpoint, c.standalone, c.touch)
		if out["name"] != c.want {
			t.Errorf("%s: имя %v, ждали %q", c.endpoint, out["name"], c.want)
		}
		if id, _ := out["id"].(float64); id == 0 {
			t.Errorf("%s: устройство не получило номер: %v", c.endpoint, out)
		}
	}
	if subs, _ := st.PushSubscriptions(); len(subs) != 4 {
		t.Fatalf("подписок %d, ждали 4", len(subs))
	}

	// страница спрашивает про себя: тот же маршрут, что отдаёт ключ
	ask := func(endpoint string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/push/key?endpoint="+url.QueryEscape(endpoint), nil)
		rec := httptest.NewRecorder()
		s.handlePushKey(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	known := ask("https://web.push.apple.com/Q1")
	if known["known"] != true || known["same_key"] != true || known["name"] != "iPhone · Сёма с экрана «Домой»" || known["key"] == "" {
		t.Errorf("известное устройство: %v", known)
	}
	if lost := ask("https://web.push.apple.com/нет"); lost["known"] != false || lost["key"] == "" {
		t.Errorf("незнакомое устройство должно получить ключ и «не знаю»: %v", lost)
	}
	if bare := ask(""); bare["known"] != nil || bare["key"] == "" {
		t.Errorf("без адреса отвечается только ключ: %v", bare)
	}

	// отписка по адресу у службы — тем же путём, что и страница
	body, _ := json.Marshal(map[string]string{"endpoint": "https://web.push.apple.com/Q1"})
	rec := httptest.NewRecorder()
	s.handlePushUnsubscribe(rec, httptest.NewRequest(http.MethodPost, "/api/push/unsubscribe", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("отписка: %d", rec.Code)
	}
	if _, ok := st.PushSubscriptionByEndpoint("https://web.push.apple.com/Q1"); ok {
		t.Error("устройство осталось после отписки")
	}
	// чужие и служебные адреса не принимаются
	for _, bad := range []string{"http://web.push.apple.com/x", "https://127.0.0.1/x"} {
		raw, _ := json.Marshal(map[string]any{"endpoint": bad, "keys": map[string]string{"p256dh": "a", "auth": "b"}})
		rec := httptest.NewRecorder()
		s.handlePushSubscribe(rec, httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(string(raw))))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("адрес %q принят: %d", bad, rec.Code)
		}
	}
}

// Устройства поимённо: список на «Уведомлениях», у каждого свои действия,
// у отказавшего — причина человеческими словами.
func TestPushDevicesListed(t *testing.T) {
	s, st := testServer(t)
	if err := st.SaveNotifyChannel(&store.NotifyChannel{Kind: "web", Title: "Пуши", Enabled: true, MinLevel: "warn", QuietFrom: "23:00", QuietTo: "8:00"}); err != nil {
		t.Fatal(err)
	}
	subscribeAs(t, s, iphoneHomeUA, "https://web.push.apple.com/Q1", true, 5)
	subscribeAs(t, s, macChromeUA, "https://fcm.googleapis.com/fcm/send/M1", false, 0)
	phone, _ := st.PushSubscriptionByEndpoint("https://web.push.apple.com/Q1")
	mac, _ := st.PushSubscriptionByEndpoint("https://fcm.googleapis.com/fcm/send/M1")
	st.MarkPushDelivery(phone.Endpoint, "Apple не приняла подпись Сёмы (403) [BadJwtToken]", 403)
	st.MarkPushDelivery(mac.Endpoint, "", 201)

	doc, err := s.buildScreen(notifySettingsScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(doc, "push-"+strconv.FormatInt(phone.ID, 10))
	if row == nil {
		t.Fatal("строки iPhone нет на экране")
	}
	if row.Subtitle != "iPhone · Сёма с экрана «Домой»" {
		t.Errorf("имя в строке: %q", row.Subtitle)
	}
	if row.Level != LevelWarn || !strings.Contains(row.Caption, "BadJwtToken") {
		t.Errorf("отказ службы не виден словами: %q %s", row.Caption, row.Level)
	}
	// порог и тихие часы — поля раскрытия строки, и подсказки говорят, чьи они: «как у
	// канала» называет, что это сейчас такое
	form := findRow(doc, "push-form-"+strconv.FormatInt(phone.ID, 10))
	if form == nil || form.Submit == nil || form.Submit.Action != "save_push_device" || form.Submit.Target != strconv.FormatInt(phone.ID, 10) {
		t.Fatalf("у устройства нет формы настроек в раскрытии: %+v", form)
	}
	fields := map[string]Field{}
	for _, f := range form.Fields {
		fields[f.Name] = f
	}
	if !strings.Contains(fields["quiet_mode"].Hint, "23:00–8:00") || !strings.Contains(fields["level"].Hint, "«внимание»") {
		t.Errorf("наследование не объяснено: %q и %q", fields["quiet_mode"].Hint, fields["level"].Hint)
	}
	if fields["level"].Type != "segments" || fields["level"].Value != pushLevelInherit || fields["quiet_mode"].Value != pushQuietInherit {
		t.Errorf("у устройства без своего порога и часов выбрано не «как у канала»: %+v %+v", fields["level"], fields["quiet_mode"])
	}
	if !row.Expandable || row.Expand != "Настройки" {
		t.Errorf("строка устройства не раскрывается подписью «Настройки»: %v %q", row.Expandable, row.Expand)
	}
	// в строке одно действие, остальное уводит в меню «···»; имя, порог и тихие часы —
	// поля, а не вопросы в окне
	var names []string
	all := append(append([]Action{}, row.Actions...), row.Menu...)
	for _, a := range all {
		names = append(names, a.Action)
		if a.Prompt != "" {
			t.Errorf("у устройства остался вопрос в окне: %+v", a)
		}
	}
	for _, want := range []string{"push_test", "push_unsubscribe"} {
		if !strings.Contains(strings.Join(names, ","), want) {
			t.Errorf("у устройства нет действия %s: %v", want, names)
		}
	}
	if len(row.Selects) != 0 {
		t.Errorf("порог устройства — поле раскрытия, а в строке остался список: %+v", row.Selects)
	}
	for _, a := range all {
		if a.Target != strconv.FormatInt(phone.ID, 10) {
			t.Errorf("действие %s нацелено на %q, ждали номер устройства", a.Action, a.Target)
		}
		if strings.Contains(a.Target, "push.apple.com") {
			t.Errorf("адрес подписки в разметке экрана: %q", a.Target)
		}
	}
	good := findRow(doc, "push-"+strconv.FormatInt(mac.ID, 10))
	if good == nil || good.Level != LevelOK || !strings.Contains(good.Caption, "последняя доставка") || !strings.Contains(good.Caption, "Google ответила 201") {
		t.Errorf("удачная доставка не видна: %+v", good)
	}
	if findRow(doc, "push-here") == nil {
		t.Error("пропала строка «Пуши на этом устройстве»")
	}
}

// Без канала web и без настоящего адреса для связи экран говорит об этом
// до списка устройств, а не обнаруживается по отказам.
func TestPushScreenWarnsAboutBreakage(t *testing.T) {
	s, st := testServer(t)
	subscribeAs(t, s, iphoneHomeUA, "https://web.push.apple.com/Q1", true, 5)
	text := func() string {
		doc, err := s.buildScreen(notifySettingsScreen, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		var all []string
		for i := range doc.Components {
			walkthrough(&doc.Components[i], func(c *Component) { all = append(all, c.Subtitle+" "+c.Caption) })
		}
		return strings.Join(all, "\n")
	}
	got := text()
	if !strings.Contains(got, "Канала «web» нет") {
		t.Error("нет канала web, а экран молчит")
	}
	if !strings.Contains(got, "Адрес для связи не задан") || !strings.Contains(got, "403 BadJwtToken") {
		t.Error("нет настоящего адреса для связи, а экран молчит про Apple")
	}
	if err := st.SaveNotifyChannel(&store.NotifyChannel{Kind: "web", Title: "Пуши", Enabled: false, MinLevel: "warn"}); err != nil {
		t.Fatal(err)
	}
	_ = st.SetSetting("auth.origin", "https://sema.example.org")
	got = text()
	if !strings.Contains(got, "выключен — пуши не уходят") {
		t.Error("канал выключен, а экран молчит")
	}
	if strings.Contains(got, "Адрес для связи не задан") {
		t.Error("адрес входа https задан, а экран жалуется на адрес для связи")
	}
	if strings.Contains(got, "Канала «web» нет") {
		t.Error("канал есть, а экран говорит, что его нет")
	}
}

// Переименование, порог, тихие часы и «Убрать» — действия экрана.
func TestPushDeviceActions(t *testing.T) {
	s, st := testServer(t)
	subscribeAs(t, s, androidUA, "https://fcm.googleapis.com/fcm/send/A1", false, 5)
	dev, _ := st.PushSubscriptionByEndpoint("https://fcm.googleapis.com/fcm/send/A1")
	id := strconv.FormatInt(dev.ID, 10)
	act := func(action string, value string) error {
		_, err := s.execute(actionRequest{Action: action, Target: id, Params: map[string]string{"value": value}})
		return err
	}
	reload := func() *store.PushSubscription { d, _ := st.PushSubscriptionByID(dev.ID); return d }

	if err := act("push_rename", "  Телефон Вадима "); err != nil {
		t.Fatal(err)
	}
	if got := reload().Title; got != "Телефон Вадима" {
		t.Errorf("имя: %q", got)
	}
	doc, _ := s.buildScreen(notifySettingsScreen, "", "", 0)
	if row := findRow(doc, "push-"+id); row == nil || row.Subtitle != "Телефон Вадима" {
		t.Errorf("новое имя не видно в списке: %+v", row)
	}
	if err := act("push_rename", ""); err != nil || reload().Title != "" {
		t.Errorf("пустое имя должно вернуть выведенное: %v %q", err, reload().Title)
	}

	if err := act("push_level", levelOptions[0]); err != nil || reload().MinLevel != "info" {
		t.Errorf("порог «инфо»: %v %q", err, reload().MinLevel)
	}
	if err := act("push_level", pushLevelInherit); err != nil || reload().MinLevel != "" {
		t.Errorf("«как у канала»: %v %q", err, reload().MinLevel)
	}

	for in, want := range map[string]string{"23:00-8:00": "23:00-8:00", "22:30–07:15": "22:30-07:15", "нет": "-", "-": "-", "": ""} {
		if err := act("push_quiet", in); err != nil || reload().Quiet != want {
			t.Errorf("тихие часы %q: %v, записано %q, ждали %q", in, err, reload().Quiet, want)
		}
	}
	for _, bad := range []string{"ночью", "25:00-8:00", "23:00-", "23:00-99:00"} {
		if err := act("push_quiet", bad); err == nil {
			t.Errorf("тихие часы %q приняты", bad)
		}
	}
	// чужого номера нет — понятная ошибка, а не молчание
	if _, err := s.execute(actionRequest{Action: "push_rename", Target: "9999", Params: map[string]string{"value": "x"}}); err == nil {
		t.Error("переименование несуществующего устройства прошло")
	}

	// «Убрать» по номеру; прежняя запись с адресом тоже понимается
	if _, err := s.execute(actionRequest{Action: "push_unsubscribe", Target: id}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.PushSubscriptionByID(dev.ID); ok {
		t.Error("устройство осталось после «Убрать»")
	}
	if _, err := s.execute(actionRequest{Action: "push_unsubscribe", Target: id}); err != nil {
		t.Errorf("повторное «Убрать» должно пройти молча: %v", err)
	}
	subscribeAs(t, s, androidUA, "https://fcm.googleapis.com/fcm/send/A2", false, 5)
	if _, err := s.execute(actionRequest{Action: "push_unsubscribe", Target: "https://fcm.googleapis.com/fcm/send/A2"}); err != nil {
		t.Fatal(err)
	}
	if subs, _ := st.PushSubscriptions(); len(subs) != 0 {
		t.Errorf("после отписки по адресу осталось %d устройств", len(subs))
	}
}

// Параметры пушей: адрес для связи проверяется, срочность и сроки хранятся.
func TestPushSettingsForm(t *testing.T) {
	s, st := testServer(t)
	save := func(p map[string]string) error {
		_, err := s.execute(actionRequest{Action: "set_push_settings", Params: p})
		return err
	}
	if err := save(map[string]string{"subject": "ivan@example.com", "ttl_min": "30", "ttl_alert_min": "720",
		"urgency_alert": "high", "urgency_warn": "low", "urgency_info": "very-low"}); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"push.subject": "mailto:ivan@example.com", "push.ttl_min": "30",
		"push.ttl_alert_min": "720", "push.urgency.alert": "high", "push.urgency.warn": "low", "push.urgency.info": "very-low"} {
		if got := st.Setting(key, ""); got != want {
			t.Errorf("%s = %q, ждали %q", key, got, want)
		}
	}
	for name, p := range map[string]map[string]string{
		"адрес из своей сети": {"subject": "https://sema.local"},
		"не адрес":            {"subject": "просто слова"},
		"срок не число":       {"ttl_min": "долго"},
		"срок ноль":           {"ttl_alert_min": "0"},
		"срочность выдумана":  {"urgency_warn": "срочно"},
	} {
		if err := save(p); err == nil {
			t.Errorf("%s: принято %v", name, p)
		}
	}
	if st.Setting("push.subject", "") != "mailto:ivan@example.com" {
		t.Error("отклонённая форма испортила настройку")
	}
	// пустое поле адреса — вернуться к адресу входа
	if err := save(map[string]string{"subject": ""}); err != nil || st.Setting("push.subject", "x") != "" {
		t.Errorf("сброс адреса: %v %q", err, st.Setting("push.subject", "x"))
	}
	// форма в «Тонкой настройке» вкладки показывает сохранённое
	form := formIn(rareBody(t, s, notifySettingsScreen), "push-settings")
	if form == nil {
		t.Fatal("формы параметров пушей нет в «Тонкой настройке»")
	}
	vals := map[string]string{}
	for _, f := range form.Fields {
		vals[f.Name] = f.Value
	}
	if vals["ttl_min"] != "30" || vals["urgency_warn"] != "low" || vals["urgency_alert"] != "high" {
		t.Errorf("форма показывает не сохранённое: %v", vals)
	}
}

// Канал можно переименовать: название «Chrome на маке» у канала, которому
// подписаны ещё и телефоны, — неправда.
func TestChannelRename(t *testing.T) {
	s, st := testServer(t)
	c := &store.NotifyChannel{Kind: "web", Title: "Chrome на маке", Enabled: true, MinLevel: "warn"}
	if err := st.SaveNotifyChannel(c); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(c.ID, 10)
	if _, err := s.execute(actionRequest{Action: "notify_channel_title", Target: id, Params: map[string]string{"value": "Пуши на устройства"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.NotifyChannel(c.ID); got.Title != "Пуши на устройства" || got.MinLevel != "warn" || !got.Enabled {
		t.Errorf("переименование задело лишнее: %+v", got)
	}
	if _, err := s.execute(actionRequest{Action: "notify_channel_title", Target: id, Params: map[string]string{"value": " "}}); err == nil {
		t.Error("пустое название принято")
	}
}

// Тумблер «Тревога висит на компьютере» — настройка, по умолчанию выключена;
// форма присылает его значение вместе с остальными полями.
func TestPushStickyAlertToggle(t *testing.T) {
	s, st := testServer(t)
	field := func() Field {
		t.Helper()
		form := formIn(rareBody(t, s, notifySettingsScreen), "push-settings")
		if form == nil {
			t.Fatal("формы параметров пушей нет")
		}
		for _, f := range form.Fields {
			if f.Name == "sticky_alert" {
				return f
			}
		}
		t.Fatal("в форме нет тумблера sticky_alert")
		return Field{}
	}
	if f := field(); f.Type != "toggle" || f.Value != "false" || !strings.Contains(f.Hint, "на телефонах не действует") {
		t.Errorf("тумблер по умолчанию: %+v", f)
	}
	save := func(p map[string]string) {
		t.Helper()
		if _, err := s.execute(actionRequest{Action: "set_push_settings", Params: p}); err != nil {
			t.Fatal(err)
		}
	}
	save(map[string]string{"sticky_alert": "true"})
	if st.Setting("push.sticky_alert", "") != "1" || field().Value != "true" {
		t.Errorf("включение не сохранилось: %q", st.Setting("push.sticky_alert", ""))
	}
	// форма без этого поля (другая форма, старый клиент) настройку не сбрасывает
	save(map[string]string{"ttl_min": "30"})
	if st.Setting("push.sticky_alert", "") != "1" {
		t.Error("непришедшее поле сбросило настройку")
	}
	// выключенный тумблер браузер не шлёт, но форма называет его в `toggles`,
	// и приходит «false»
	save(map[string]string{"sticky_alert": "false"})
	if st.Setting("push.sticky_alert", "") != "0" || field().Value != "false" {
		t.Errorf("выключение не сохранилось: %q", st.Setting("push.sticky_alert", ""))
	}
}

// Предложение подписаться на пуши — наверху «Ждут вас»: туда человек попадает
// одним касанием. Строка приезжает скрытой (открывает её клиент, когда знает
// состояние устройства), «Получать пуши» — основная кнопка, «Не предлагать» —
// в меню «···». В «Настройки → Уведомления» своя строка остаётся.
func TestPushOfferOnNotifications(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen(notifyScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	offer := findRow(doc, "push-offer")
	if offer == nil {
		t.Fatal("на экране «Уведомления» нет строки предложения подписаться на пуши")
	}
	if offer.Style != "offer" {
		t.Errorf("строка не помечена скрытой по умолчанию (класс offer): %q", offer.Style)
	}
	if len(offer.Actions) != 1 || offer.Actions[0].Action != "$push" || offer.Actions[0].Style != "primary" || offer.Actions[0].Label != "Получать пуши" {
		t.Errorf("основная кнопка предложения: %+v", offer.Actions)
	}
	if len(offer.Menu) != 1 || offer.Menu[0].Action != "$push_later" || offer.Menu[0].Label != "Не предлагать на этом устройстве" {
		t.Errorf("«Не предлагать» должно лежать в меню строки: %+v", offer.Menu)
	}
	// наверху списка: раньше первой группы записей
	if len(doc.Components) == 0 || doc.Components[0].ID != "push-offer" {
		t.Errorf("предложение не стоит первым: %+v", doc.Components[0])
	}

	// в разметке: скрытый класс, основная кнопка и пункт меню с клиентским действием
	html, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{`class="row offer`, `data-action="$push"`, `data-action="$push_later"`, `class="row-menu"`} {
		if !strings.Contains(page, want) {
			t.Errorf("в разметке нет %q", want)
		}
	}

	// на вкладке «История» и в разрезе по источнику предложения нет: оно для главного списка
	for _, filter := range []string{"done", "servers", "mail"} {
		d, err := s.buildScreen(notifyScreen+"@"+filter, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if findRow(d, "push-offer") != nil {
			t.Errorf("предложение попало на вкладку %q", filter)
		}
	}

	// строка в настройках осталась и скрытой не рисуется
	set, err := s.buildScreen(notifySettingsScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	here := findRow(set, "push-here")
	if here == nil || here.Style == "offer" || len(here.Actions) != 1 || here.Actions[0].Action != "$push" {
		t.Errorf("строка в «Настройки → Уведомления» изменилась: %+v", here)
	}
	if findRow(set, "push-offer") != nil {
		t.Error("предложение попало в настройки: там своя строка, видная всегда")
	}
}

// Уведомления «Устройство отписалось» и «Переподпишите»: кнопка подписки
// лежит на «Ждут вас», где эти записи и стоят, а переход ведёт к списку
// устройств; в тексте сказано, где кнопка.
func TestPushNoticesPointToTheOffer(t *testing.T) {
	s, st := testServer(t)
	subscribeAs(t, s, iphoneHomeUA, "https://web.push.apple.com/Q1", true, 5)
	dev, _ := st.PushSubscriptionByEndpoint("https://web.push.apple.com/Q1")
	if err := st.SavePushSubscription(&store.PushSubscription{Endpoint: dev.Endpoint, P256dh: dev.P256dh, Auth: dev.Auth,
		VAPIDKey: "старый-ключ", Agent: dev.Agent}); err != nil {
		t.Fatal(err)
	}
	// ключ сменился: запись «Переподпишите» заводит сама отправка
	if _, _, _, same := s.eng.PushDeviceStatus(dev.Endpoint); same {
		t.Fatal("ключ не считается сменившимся")
	}
	if _, err := s.eng.TestPushDevice(dev.ID); err == nil {
		t.Fatal("проба по подписке под чужим ключом прошла")
	}
	n, ok := st.OpenNotificationByKey("push:resub:" + strconv.FormatInt(dev.ID, 10))
	if !ok {
		t.Fatal("записи «Переподпишите» нет")
	}
	if !strings.Contains(n.Summary, "«Ждут вас»") || !strings.Contains(n.Summary, "наверху списка") {
		t.Errorf("в тексте не сказано, где кнопка: %q", n.Summary)
	}
	if len(n.Actions) != 1 || n.Actions[0].Screen != notifySettingsScreen || n.Actions[0].Label != "Устройства" {
		t.Errorf("переход ведёт не к списку устройств: %+v", n.Actions)
	}
}
