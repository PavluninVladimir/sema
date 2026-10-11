package api

// «Уведомления → Каналы доставки» (2 октября 2026): настройки канала и устройства
// раскрываются на месте полями вместо вопросов в окне; сохраняет нижняя полоса;
// доставка учитывает сохранённое. Правила почты, «Что показывать», провод и опрос не
// трогались.

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// stnWebChannel — канал «Chrome на маке», который заводит stnFix.
func stnWebChannel(t *testing.T, st *store.Store) *store.NotifyChannel {
	t.Helper()
	list, err := st.NotifyChannels()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.Kind == "web" {
			return c
		}
	}
	t.Fatal("канала web нет")
	return nil
}

// У строки канала «Настройки» раскрываются на месте: поля вместо вопросов, в самой
// строке — тумблер, «Проверить» и «···».
func TestChannelRowsHaveSettingsInPlace(t *testing.T) {
	s, st := stnFix(t)
	doc := mustDoc(t, s, notifySettingsScreen)
	chans, _ := st.NotifyChannels()
	if len(chans) < 2 {
		t.Fatalf("каналов %d, ждали Mac и пуши", len(chans))
	}
	for _, c := range chans {
		id := strconv.FormatInt(c.ID, 10)
		row := findRow(doc, "channel-"+id)
		if row == nil {
			t.Fatalf("строки канала %q нет", c.Title)
		}
		if !row.Expandable || row.Expand != "Настройки" || len(row.Children) != 1 {
			t.Errorf("%s: «Настройки» не раскрываются на месте: %+v", c.Title, row)
		}
		if len(row.Badges) > 2 {
			t.Errorf("%s: капсул %d", c.Title, len(row.Badges))
		}
		if n := stnVisibleButtons(row); n != 1 || row.Actions[len(row.Actions)-1].Action != "test_notify_channel" {
			t.Errorf("%s: в строке кнопок %d (ждали «Проверить»): %+v", c.Title, n, row.Actions)
		}
		if row.Toggle == "" || len(row.Menu) != 1 || row.Menu[0].Action != "delete_notify_channel" || row.Menu[0].Confirm == "" || row.Menu[0].ConfirmOK == "" {
			t.Errorf("%s: тумблер и «···» с «Удалить…»: %+v", c.Title, row)
		}
		form := row.Children[0]
		if form.Type != "form" || form.Submit.Action != "save_notify_channel" || form.Submit.Target != id || !form.Deferred || form.Style != formRows {
			t.Fatalf("%s: форма настроек: %+v", c.Title, form)
		}
		types := map[string]string{}
		for _, f := range form.Fields {
			types[f.Name] = f.Type
		}
		if types["title"] != "text" || types["level"] != "segments" || types["sources"] != "checks" || types["quiet"] != "timespan" || len(types) != 4 {
			t.Errorf("%s: поля формы: %v", c.Title, types)
		}
	}
	// вопросов в окне и списка порога в строке больше нет ни у канала, ни у устройства
	var walk func(cs []Component)
	walk = func(cs []Component) {
		for _, c := range cs {
			for _, a := range append(append([]Action{}, c.Actions...), c.Menu...) {
				if a.Prompt != "" {
					t.Errorf("остался вопрос в окне: %s %q у %q", a.Action, a.Prompt, c.Subtitle)
				}
			}
			if strings.HasPrefix(c.ID, "channel-") || strings.HasPrefix(c.ID, "push-") {
				for _, sel := range c.Selects {
					t.Errorf("у %s остался список в строке: %+v", c.ID, sel)
				}
			}
			walk(c.Children)
		}
	}
	walk(doc.Components)
	// значения полей — те, что в канале: порог словом, источники отмеченными, часы как были
	web := stnWebChannel(t, st)
	form := formIn(doc.Components, "channel-form-"+strconv.FormatInt(web.ID, 10))
	vals := map[string]string{}
	for _, f := range form.Fields {
		vals[f.Name] = f.Value
	}
	if vals["level"] != "внимание" || vals["sources"] != "Серверы,Почта" || vals["quiet"] != "23:00-8:00" || vals["title"] != "Chrome на маке" {
		t.Errorf("поля канала показывают не то, что в канале: %v", vals)
	}
	// «Добавить канал» — кнопка шапки вкладки, формы под списком нет; другие вкладки без неё
	if len(doc.ToolbarActions) != 1 || doc.ToolbarActions[0].Label != "Добавить канал" || doc.ToolbarActions[0].Target != entityAddr(notifySettingsScreen, notifyNew, "") {
		t.Errorf("кнопки шапки: %+v", doc.ToolbarActions)
	}
	if findRow(doc, "channel-new") != nil {
		t.Error("форма «Добавить канал» осталась под списком")
	}
	for _, tab := range []string{notifyTabRules, notifyTabShow} {
		if d := mustDoc(t, s, notifySettingsScreen+"~"+tab); len(d.ToolbarActions) != 0 {
			t.Errorf("вкладка %q: в шапке появились кнопки %+v", tab, d.ToolbarActions)
		}
	}
	// у устройства — свои «Настройки» в раскрытии
	subs, _ := st.PushSubscriptions()
	for _, p := range subs {
		row := findRow(doc, "push-"+strconv.FormatInt(p.ID, 10))
		if row == nil || !row.Expandable || row.Expand != "Настройки" || row.Children[0].Submit.Action != "save_push_device" {
			t.Errorf("устройство %d без настроек в раскрытии: %+v", p.ID, row)
		}
	}
	// капсулы устройства — только то, что отличает его от канала
	phone, _ := st.PushSubscriptionByEndpoint("https://web.push.apple.com/Q1")
	phoneRow := findRow(doc, "push-"+strconv.FormatInt(phone.ID, 10))
	texts := []string{}
	for _, b := range phoneRow.Badges {
		texts = append(texts, b.Text)
	}
	if strings.Join(texts, "|") != "свой порог: инфо|свои тихие часы" {
		t.Errorf("капсулы устройства со своим порогом и часами: %v", texts)
	}
	mac, _ := st.PushSubscriptionByEndpoint("https://fcm.googleapis.com/fcm/send/M1")
	if row := findRow(doc, "push-"+strconv.FormatInt(mac.ID, 10)); len(row.Badges) != 0 {
		t.Errorf("у устройства «как у канала» капсул нет, а есть %+v", row.Badges)
	}
}

// Порог, источники и тихие часы сохраняются полями, а доставка их учитывает: запись ниже
// порога каналу не идёт, чужой источник — тоже, тихие часы — тоже. Спрашивается движок, а
// не пересчёт на месте.
func TestChannelSavedByFieldsAndDeliveryHonorsIt(t *testing.T) {
	s, st := stnFix(t)
	web := stnWebChannel(t, st)
	id := strconv.FormatInt(web.ID, 10)
	save := func(p map[string]string) error {
		_, err := s.execute(actionRequest{Action: "save_notify_channel", Target: id, Params: p})
		return err
	}
	reload := func() *store.NotifyChannel { c, _ := st.NotifyChannel(web.ID); return c }
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local)
	note := func(level, source string) *store.Notification {
		return &store.Notification{Key: "проба:" + level + source, Source: source, Level: level, Title: "проба"}
	}
	// запись про сервер: группа «servers», которую канал из stnFix берёт
	serverNote := func(level string) *store.Notification {
		n := note(level, "core")
		n.Key = "server:srv-vpn"
		return n
	}
	takes := func(c *store.NotifyChannel, n *store.Notification, at time.Time) string {
		return engine.ChannelTakes(c, n, at)
	}

	// порог «тревога»: внимание не идёт, тревога идёт
	if err := save(map[string]string{"level": "тревога"}); err != nil {
		t.Fatal(err)
	}
	c := reload()
	if c.MinLevel != "alert" {
		t.Fatalf("порог после сохранения: %q", c.MinLevel)
	}
	if why := takes(c, serverNote("warn"), now); why != "порог канала выше уровня записи" {
		t.Errorf("запись «внимание» при пороге «тревога»: %q", why)
	}
	if why := takes(c, serverNote("alert"), now); why != "" {
		t.Errorf("тревога при пороге «тревога» не идёт: %q", why)
	}
	// порог «инфо» пропускает всё
	if err := save(map[string]string{"level": "инфо"}); err != nil {
		t.Fatal(err)
	}
	if why := takes(reload(), serverNote("info"), now); why != "" {
		t.Errorf("«инфо» при пороге «инфо»: %q", why)
	}
	// источники: отмечены «Серверы» — почтовое не идёт, серверное идёт
	if err := save(map[string]string{"sources.Серверы": "true", "sources.Почта": "false", "sources.Запросы": "false", "sources.Модули": "false"}); err != nil {
		t.Fatal(err)
	}
	c = reload()
	if c.Sources != "servers" {
		t.Fatalf("источники после сохранения: %q", c.Sources)
	}
	if why := takes(c, note("alert", "mail"), now); !strings.Contains(why, "источник «mail» не входит") {
		t.Errorf("почтовая запись при источнике «Серверы»: %q", why)
	}
	srv := note("alert", "core")
	srv.Key = "server:srv-vpn"
	if why := takes(c, srv, now); why != "" {
		t.Errorf("запись про сервер при источнике «Серверы»: %q", why)
	}
	// отмечены все — поле пусто, берёт всё, как без списка
	if err := save(map[string]string{"sources.Серверы": "true", "sources.Почта": "true", "sources.Запросы": "true", "sources.Модули": "true"}); err != nil {
		t.Fatal(err)
	}
	if got := reload().Sources; got != "" {
		t.Errorf("все четыре источника записаны как %q, а должно быть пусто — «все»", got)
	}
	// две из четырёх — в порядке экрана
	if err := save(map[string]string{"sources.Модули": "true", "sources.Почта": "true", "sources.Серверы": "false", "sources.Запросы": "false"}); err != nil {
		t.Fatal(err)
	}
	if got := reload().Sources; got != "mail,modules" {
		t.Errorf("два источника: %q", got)
	}
	// ни одного — отказ с объяснением, ничего не пишется
	err := save(map[string]string{"sources.Модули": "false", "sources.Почта": "false", "sources.Серверы": "false", "sources.Запросы": "false"})
	if err == nil || !strings.Contains(err.Error(), "хотя бы один") {
		t.Errorf("пустой список источников принят: %v", err)
	}
	if got := reload().Sources; got != "mail,modules" {
		t.Errorf("отказанная форма изменила источники: %q", got)
	}
	// тихие часы: в окно запись не идёт, вне окна — идёт; пусто — часов нет
	from, to := now.Add(-time.Hour).Format("15:04"), now.Add(time.Hour).Format("15:04")
	if err := save(map[string]string{"quiet_from": from, "quiet_to": to}); err != nil {
		t.Fatal(err)
	}
	c = reload()
	if c.QuietFrom != from || c.QuietTo != to {
		t.Fatalf("тихие часы после сохранения: %q–%q", c.QuietFrom, c.QuietTo)
	}
	mailNote := note("alert", "mail")
	if why := takes(c, mailNote, now); !strings.Contains(why, "тихие часы") {
		t.Errorf("запись в тихие часы: %q", why)
	}
	if why := takes(c, mailNote, now.Add(5*time.Hour)); why != "" {
		t.Errorf("запись вне тихих часов: %q", why)
	}
	if err := save(map[string]string{"quiet_from": "", "quiet_to": ""}); err != nil {
		t.Fatal(err)
	}
	if c = reload(); c.QuietFrom != "" || c.QuietTo != "" {
		t.Errorf("пустые поля не сняли тихие часы: %q–%q", c.QuietFrom, c.QuietTo)
	}
	// плохое: половина окна, совпадающие концы, не время, пустое название
	for name, p := range map[string]map[string]string{
		"половина окна":   {"quiet_from": "23:00", "quiet_to": ""},
		"концы совпадают": {"quiet_from": "08:00", "quiet_to": "08:00"},
		"не время":        {"quiet_from": "ночью", "quiet_to": "08:00"},
		"пустое название": {"title": "  "},
	} {
		if err := save(p); err == nil {
			t.Errorf("%s: принято %v", name, p)
		}
	}
	// переименование и непришедшие поля: остальное не тронуто
	if err := save(map[string]string{"title": "  Пуши на устройства "}); err != nil {
		t.Fatal(err)
	}
	if c = reload(); c.Title != "Пуши на устройства" || c.MinLevel != "info" || c.Sources != "mail,modules" || !c.Enabled {
		t.Errorf("переименование задело лишнее: %+v", c)
	}
	// чужого канала нет — понятная ошибка
	if _, err := s.execute(actionRequest{Action: "save_notify_channel", Target: "9999", Params: map[string]string{"title": "x"}}); err == nil {
		t.Error("несуществующий канал сохранён")
	}
	// после сохранения экран показывает записанное
	doc := mustDoc(t, s, notifySettingsScreen)
	form := formIn(doc.Components, "channel-form-"+id)
	vals := map[string]string{}
	for _, f := range form.Fields {
		vals[f.Name] = f.Value
	}
	if vals["title"] != "Пуши на устройства" || vals["level"] != "инфо" || vals["sources"] != "Почта,Модули" || vals["quiet"] != "" {
		t.Errorf("экран показывает не записанное: %v", vals)
	}
}

// Прежние действия с вопросами остались: на них смотрят скрипты и карточки.
func TestChannelActionsWithoutScreenStillWork(t *testing.T) {
	s, st := stnFix(t)
	web := stnWebChannel(t, st)
	id := strconv.FormatInt(web.ID, 10)
	act := func(action, value string) error {
		_, err := s.execute(actionRequest{Action: action, Target: id, Params: map[string]string{"value": value}})
		return err
	}
	reload := func() *store.NotifyChannel { c, _ := st.NotifyChannel(web.ID); return c }
	if err := act("notify_channel_level", levelOptions[2]); err != nil || reload().MinLevel != "alert" {
		t.Errorf("notify_channel_level: %v %q", err, reload().MinLevel)
	}
	if err := act("notify_channel_quiet", "22:00-6:30"); err != nil || reload().QuietFrom != "22:00" || reload().QuietTo != "6:30" {
		t.Errorf("notify_channel_quiet: %v %q–%q", err, reload().QuietFrom, reload().QuietTo)
	}
	if err := act("notify_channel_quiet", ""); err != nil || reload().QuietFrom != "" {
		t.Errorf("пустые тихие часы не сняты: %v", err)
	}
	if err := act("notify_channel_quiet", "ночью"); err == nil {
		t.Error("тихие часы «ночью» приняты")
	}
	if err := act("notify_channel_sources", "servers, mail"); err != nil || reload().Sources != "servers,mail" {
		t.Errorf("notify_channel_sources: %v %q", err, reload().Sources)
	}
	if err := act("notify_channel_title", "Другое имя"); err != nil || reload().Title != "Другое имя" {
		t.Errorf("notify_channel_title: %v %q", err, reload().Title)
	}
	// «Проверить» не-web канала: запись «проверка», а не вопрос
	if _, err := s.execute(actionRequest{Action: "toggle_notify_channel", Target: id}); err != nil || reload().Enabled {
		t.Errorf("toggle_notify_channel: %v", err)
	}
	if _, err := s.execute(actionRequest{Action: "delete_notify_channel", Target: id}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.NotifyChannel(web.ID); ok {
		t.Error("канал остался после «Удалить»")
	}
}

// «Добавить канал» — свой экран: вид, название, настройки вида (JSON); что вписано — то и
// уйдёт в `add_notify_channel`; после — список каналов.
func TestChannelAddScreen(t *testing.T) {
	s, st := stnFix(t)
	doc := mustDoc(t, s, entityAddr(notifySettingsScreen, notifyNew, ""))
	form := formIn(doc.Components, "channel-new")
	if form == nil || form.Submit.Action != "add_notify_channel" || form.Deferred {
		t.Fatalf("форма добавления: %+v", form)
	}
	names := []string{}
	for _, f := range form.Fields {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "kind,title,config" {
		t.Errorf("поля добавления: %v", names)
	}
	if doc.Components[0].Type != "crumb" {
		t.Errorf("у экрана добавления нет «‹ Уведомления»: %+v", doc.Components[0])
	}
	before, _ := st.NotifyChannels()
	next, err := s.execute(actionRequest{Action: "add_notify_channel", Params: map[string]string{"kind": "mac", "title": "Мак ещё раз"}})
	if err != nil || next != notifySettingsScreen {
		t.Fatalf("add_notify_channel: %q %v", next, err)
	}
	after, _ := st.NotifyChannels()
	if len(after) != len(before)+1 {
		t.Fatalf("каналов было %d, стало %d", len(before), len(after))
	}
	added := after[len(after)-1]
	if added.Title != "Мак ещё раз" || !added.Enabled || added.MinLevel != "warn" {
		t.Errorf("новый канал: %+v", added)
	}
	// пустое название — название по виду, как было
	if _, err := s.execute(actionRequest{Action: "add_notify_channel", Params: map[string]string{"kind": "web"}}); err != nil {
		t.Fatal(err)
	}
	// у экрана добавления нет «Тонкой настройки» и ни одного вопроса в окне
	if findRow(doc, rareID) != nil {
		t.Error("на экране добавления канала «Тонкая настройка»")
	}
}

// Устройство с пушами: имя, порог и тихие часы — поля раскрытия; три состояния тихих часов.
func TestPushDeviceSavedByFields(t *testing.T) {
	s, st := stnFix(t)
	mac, _ := st.PushSubscriptionByEndpoint("https://fcm.googleapis.com/fcm/send/M1")
	id := strconv.FormatInt(mac.ID, 10)
	save := func(p map[string]string) error {
		_, err := s.execute(actionRequest{Action: "save_push_device", Target: id, Params: p})
		return err
	}
	reload := func() *store.PushSubscription { d, _ := st.PushSubscriptionByID(mac.ID); return d }

	// имя, порог «тревога» и свои тихие часы
	if err := save(map[string]string{"title": "  Мак на работе ", "level": "тревога", "quiet_mode": pushQuietOwn, "quiet_from": "21:00", "quiet_to": "07:30"}); err != nil {
		t.Fatal(err)
	}
	d := reload()
	if d.Title != "Мак на работе" || d.MinLevel != "alert" || d.Quiet != "21:00-07:30" {
		t.Errorf("после сохранения: %+v", d)
	}
	// экран показывает записанное
	form := formIn(mustDoc(t, s, notifySettingsScreen).Components, "push-form-"+id)
	vals := map[string]string{}
	for _, f := range form.Fields {
		vals[f.Name] = f.Value
	}
	if vals["title"] != "Мак на работе" || vals["level"] != "тревога" || vals["quiet_mode"] != pushQuietOwn || vals["quiet"] != "21:00-07:30" {
		t.Errorf("экран показывает не записанное: %v", vals)
	}
	// «без тихих часов» — дефис в записи, окно игнорируется
	if err := save(map[string]string{"quiet_mode": pushQuietNone, "quiet_from": "21:00", "quiet_to": "07:30"}); err != nil {
		t.Fatal(err)
	}
	if got := reload().Quiet; got != "-" {
		t.Errorf("«без тихих часов» записано как %q", got)
	}
	// «как у канала» — пусто, и порог «как у канала» — пусто
	if err := save(map[string]string{"quiet_mode": pushQuietInherit, "level": pushLevelInherit}); err != nil {
		t.Fatal(err)
	}
	if d := reload(); d.Quiet != "" || d.MinLevel != "" {
		t.Errorf("«как у канала»: %+v", d)
	}
	// «свои» без времени — отказ, и ничего не пишется (имя тоже)
	if err := save(map[string]string{"title": "Не должно записаться", "quiet_mode": pushQuietOwn, "quiet_from": "", "quiet_to": ""}); err == nil {
		t.Error("«свои» без времени приняты")
	}
	if got := reload().Title; got != "Мак на работе" {
		t.Errorf("отказанная форма записала имя: %q", got)
	}
	// непришедшие поля — «не трогали»
	if err := save(map[string]string{"title": ""}); err != nil {
		t.Fatal(err)
	}
	if d := reload(); d.Title != "" || d.MinLevel != "" || d.Quiet != "" {
		t.Errorf("пустое имя должно вернуть выведенное, остальное — не тронуто: %+v", d)
	}
	if err := save(map[string]string{"level": "что-то"}); err != nil {
		t.Logf("непонятный порог: %v", err)
	}
	if err := save(map[string]string{"quiet_mode": pushQuietOwn, "quiet_from": "25:00", "quiet_to": "07:30"}); err == nil {
		t.Error("время 25:00 принято")
	}
	if _, err := s.execute(actionRequest{Action: "save_push_device", Target: "9999", Params: map[string]string{"title": "x"}}); err == nil {
		t.Error("несуществующее устройство сохранено")
	}
}

// Параметры пушей — «Тонкая настройка» вкладки, а не форма на ней; значения те же, ключи
// те же, и движок читает сохранённое.
func TestPushParamsAreFineSettingsAndEngineReadsThem(t *testing.T) {
	s, st := stnFix(t)
	doc := mustDoc(t, s, notifySettingsScreen)
	if formIn(doc.Components, "push-settings") != nil {
		t.Error("форма параметров пушей осталась на самом экране")
	}
	fold := rareFoldOf(t, s, notifySettingsScreen)
	if !strings.HasPrefix(fold.Caption, "7 параметров") {
		t.Errorf("подпись «Тонкой настройки»: %q", fold.Caption)
	}
	if _, err := s.execute(actionRequest{Action: "set_push_settings", Params: map[string]string{
		"subject": "ivan@example.com", "ttl_min": "30", "ttl_alert_min": "720", "sticky_alert": "true",
		"urgency_alert": "very-low", "urgency_warn": "low", "urgency_info": "normal"}}); err != nil {
		t.Fatal(err)
	}
	if subject, real := s.eng.PushSubject(); subject != "mailto:ivan@example.com" || !real {
		t.Errorf("движок читает адрес для связи %q (%v)", subject, real)
	}
	if !s.eng.PushStickyAlert() {
		t.Error("движок не видит «тревога висит»")
	}
	// ключи прежние
	for key, want := range map[string]string{"push.ttl_min": "30", "push.ttl_alert_min": "720", "push.sticky_alert": "1",
		"push.urgency.alert": "very-low", "push.urgency.warn": "low"} {
		if got := st.Setting(key, ""); got != want {
			t.Errorf("%s = %q, ждали %q", key, got, want)
		}
	}
	// поле срочности — список слов службы, и непонятное значение показывает умолчание
	_ = st.SetSetting("push.urgency.warn", "срочно")
	fields := rareFieldsOf(rareBody(t, s, notifySettingsScreen))
	if f := fields["urgency_warn"]; f.Type != "picker" || len(f.Options) != 4 || f.Value != "normal" {
		t.Errorf("срочность «внимания» при мусоре в базе: %+v", f)
	}
}

// Условия, без которых канал пушей невозможен, названы там, где его включают, и у замечания
// есть действие, которое ведёт прямо к настройке: без https пуши не заработают — «Доступ».
func TestChannelWithoutHTTPSSaysSoAndLeadsToAccess(t *testing.T) {
	s, st := stnFix(t)
	_ = st.SetSetting("auth.origin", "")
	doc := mustDoc(t, s, notifySettingsScreen)
	row := findRow(doc, "push-https")
	if row == nil || row.Level != LevelAlert || len(row.Actions) != 1 || row.Actions[0].Target != accessScreen || row.Actions[0].Style != "primary" {
		t.Fatalf("замечание про https и переход к «Доступу»: %+v", row)
	}
	web := stnWebChannel(t, st)
	chRow := findRow(doc, "channel-"+strconv.FormatInt(web.ID, 10))
	if len(chRow.Badges) == 0 || chRow.Badges[0].Text != "без https не заработает" || chRow.Level != LevelAlert {
		t.Errorf("строка канала не говорит про https: %+v", chRow)
	}
	_ = st.SetSetting("auth.origin", "https://sema.example.org")
	if findRow(mustDoc(t, s, notifySettingsScreen), "push-https") != nil {
		t.Error("адрес входа https задан, а замечание осталось")
	}
}

// Вспомогательные разборы: чипы-флажки, источники, тихие часы.
func TestNotifyFieldHelpers(t *testing.T) {
	opts := notifySourceOptions()
	if strings.Join(opts, ",") != "Серверы,Почта,Запросы,Модули" {
		t.Fatalf("варианты источников: %v", opts)
	}
	// выбранные по записи канала: пусто — все
	if got := strings.Join(sourcesChosen(""), ","); got != "Серверы,Почта,Запросы,Модули" {
		t.Errorf("пустое поле — все источники: %q", got)
	}
	if got := strings.Join(sourcesChosen("mail, servers"), ","); got != "Серверы,Почта" {
		t.Errorf("источники из записи: %q", got)
	}
	// чипы-флажки: ни одного пришедшего — «не трогали»; «false» — снят
	if _, present := checkedParams(map[string]string{"level": "x"}, "sources", opts); present {
		t.Error("поле «пришло», хотя флажков нет")
	}
	chosen, present := checkedParams(map[string]string{"sources.Почта": "true", "sources.Серверы": "false"}, "sources", opts)
	if !present || strings.Join(chosen, ",") != "Почта" {
		t.Errorf("чипы-флажки: %v %v", chosen, present)
	}
	// слова сегментов порога
	for level, word := range map[string]string{"info": "инфо", "warn": "внимание", "alert": "тревога", "": "внимание"} {
		if got := notifyLevelWord(level); got != word {
			t.Errorf("порог %q словом: %q", level, got)
		}
		if got := levelFromOption(word); level != "" && got != level {
			t.Errorf("слово %q обратно: %q", word, got)
		}
	}
	// тихие часы одним значением и обратно
	if got := quietSpan("23:00", "8:00"); got != "23:00-8:00" {
		t.Errorf("quietSpan: %q", got)
	}
	if quietSpan("23:00", "") != "" || quietSpan("", "") != "" {
		t.Error("половина окна — не окно")
	}
}
