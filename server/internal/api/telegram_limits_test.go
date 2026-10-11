package api

// «Telegram → Ответы и пределы» (2 октября 2026): значения, которые правились
// текстовым вопросом («раз в», «за заход», срок поста, слово вызова и прочие
// пределы), — поля строками настройки; сохраняет нижняя полоса. Ключи настроек и
// умолчания прежние: поле берёт их из реестра, а читает их тот же реестр, что и
// движок. Прежние действия с вопросами остались ради скриптов и старых карточек.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// tgLimitFields — поле формы, ключ настройки и на какой форме оно лежит: на самой
// вкладке (plain) или в «Тонкой настройке» (rare).
var tgLimitFields = []struct {
	field, key string
	rare       bool
}{
	{"help_word", "tg.help_word", false},
	{"reply_max_hour", "tg.reply_max_hour", false},
	{"wait_warn_min", "tg.wait_warn_min", false},
	{"wait_alert_min", "tg.wait_alert_min", false},
	{"wait_msgs", "tg.wait_msgs", false},
	{"history", "tg.history", true},
	{"file_limit_mb", "tg.file_limit_mb", true},
	{"post_days", "tg.post_days", true},
	{"chats_max", "tg.chats_max", true},
	{"stream_ms", "tg.stream_ms", true},
	{"stream_first", "tg.stream_first", true},
}

func tgAnswerScreen(a string) string { return tgScreen + "@" + a + "~" + tgTabAnswer }

func TestTelegramAnswerFieldsHaveRegistryDefaults(t *testing.T) {
	s, st := telegramWithChannels(t, 0)
	// фикстура гасит историю, чтобы тест не ходил в Telegram; здесь нужна пустая база
	st.DeleteSettingKey("tg.history")
	screen := tgAnswerScreen(tgDigits)
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	plain := rareFieldsOf(doc.Components)
	fine := rareFieldsOf(rareBody(t, s, screen))
	for _, f := range tgLimitFields {
		def, ok := store.LookupSetting(f.key)
		if !ok {
			t.Errorf("настройки %s нет в реестре", f.key)
			continue
		}
		where, other := plain, fine
		if f.rare {
			where, other = fine, plain
		}
		field, here := where[f.field]
		if !here {
			t.Errorf("поля %q нет там, где ему место (редкое=%v)", f.field, f.rare)
			continue
		}
		if _, dup := other[f.field]; dup {
			t.Errorf("поле %q лежит и в «Тонкой настройке», и на самой вкладке", f.field)
		}
		if field.Default == nil || *field.Default != def.ShownDefault() || field.Value != *field.Default {
			t.Errorf("%s: умолчание поля %v, реестра %q, значение на пустой базе %q", f.field, field.Default, def.ShownDefault(), field.Value)
		}
	}
	// пределы обращений учётки — из самой учётки, умолчание — то, что подставляет хранилище
	for name, want := range map[string]int{"every": store.TGEveryDefault, "batch": store.TGBatchDefault} {
		field, ok := fine[name]
		if !ok || field.Default == nil || *field.Default != strconv.Itoa(want) {
			t.Errorf("поле %q учётки: %+v", name, field)
		}
	}
	// подпись «Тонкой настройки»: восемь полей и две учётных (раз в, за заход), всё по умолчанию…
	// на учётке, заведённой с пределами 1 и 50, они не по умолчанию — «изменены»
	fold := rareFoldOf(t, s, screen)
	if !strings.HasPrefix(fold.Caption, "8 параметров") {
		t.Errorf("«Тонкая настройка»: %q", fold.Caption)
	}
	if fold.Meta != "2 изменены" {
		t.Errorf("учётка с пределами 1 и 50 — это два изменённых, а подпись %q", fold.Meta)
	}
	// ни одного вопроса в окне на вкладке: Prompt заменён полями
	var walk func([]Component)
	walk = func(cs []Component) {
		for _, c := range cs {
			for _, a := range append(append([]Action{}, c.Actions...), c.Menu...) {
				if a.Prompt != "" {
					t.Errorf("на вкладке остался вопрос в окне: %s (%q)", a.Action, a.Prompt)
				}
			}
			walk(c.Children)
		}
	}
	walk(doc.Components)
	walk(rareBody(t, s, screen))
}

// Каждое поле сохраняется своей формой, а движок и реестр читают то же значение;
// возврат умолчания тем же жестом возвращает умолчание.
func TestTelegramLimitFieldsSaveAndEngineReads(t *testing.T) {
	s, st := telegramWithChannels(t, 0)
	screen := tgAnswerScreen(tgDigits)
	engineRead := map[string]func() string{
		"tg.reply_max_hour": func() string { return strconv.Itoa(s.eng.TGReplyMaxHour()) },
		"tg.history":        func() string { return strconv.Itoa(s.eng.TGHistoryLimit()) },
		"tg.file_limit_mb":  func() string { return strconv.Itoa(s.eng.TGFileLimitMB()) },
		"tg.help_word":      s.eng.TGHelpWord,
	}
	other := func(def store.SettingDef) string {
		if def.Kind == store.SettingText {
			return "бот"
		}
		return strconv.Itoa(def.DefaultInt() + 7)
	}
	for _, f := range tgLimitFields {
		def, _ := store.LookupSetting(f.key)
		body := rareBody(t, s, screen)
		doc, _ := s.buildScreen(screen, "", "", 0)
		form := rareFormOf(append(append([]Component{}, doc.Components...), body...), f.field)
		if form == nil || form.Submit == nil || form.Submit.Action != "save_tg_limits" {
			t.Errorf("%s: у поля нет формы с действием сохранения: %+v", f.field, form)
			continue
		}
		alt := other(def)
		if _, err := s.execute(actionRequest{Action: form.Submit.Action, Target: form.Submit.Target, Screen: screen,
			Params: map[string]string{f.field: alt}}); err != nil {
			t.Errorf("%s = %q не сохранилось: %v", f.field, alt, err)
			continue
		}
		if got := st.SettingShown(def); got != alt {
			t.Errorf("%s: сохранено %q, а в базе читается %q", f.field, alt, got)
		}
		if read, ok := engineRead[f.key]; ok && read() != alt {
			t.Errorf("%s: сохранено %q, а движок читает %q", f.field, alt, read())
		}
		after, _ := s.buildScreen(screen, "", "", 0)
		shown := rareFieldsOf(append(append([]Component{}, after.Components...), rareBody(t, s, screen)...))[f.field]
		if shown.Value != alt {
			t.Errorf("%s: экран показывает %q, а сохранено %q", f.field, shown.Value, alt)
		}
		// возврат умолчания тем же действием
		if _, err := s.execute(actionRequest{Action: form.Submit.Action, Target: form.Submit.Target, Screen: screen,
			Params: map[string]string{f.field: def.ShownDefault()}}); err != nil {
			t.Errorf("%s: возврат умолчания: %v", f.field, err)
		}
		if got := st.SettingShown(def); got != def.ShownDefault() {
			t.Errorf("%s: после возврата читается %q", f.field, got)
		}
	}
	// пределы обращений адресованы учётке и пишутся в неё
	if _, err := s.execute(actionRequest{Action: "save_tg_rate", Target: tgDigits, Params: map[string]string{"every": "7", "batch": "70"}}); err != nil {
		t.Fatal(err)
	}
	acc, _ := st.TGAccount(telegramAccount)
	if acc.Every != 7 || acc.Batch != 70 {
		t.Errorf("пределы учётки: %d и %d", acc.Every, acc.Batch)
	}
	// одно поле из двух: второе не сбрасывается
	if _, err := s.execute(actionRequest{Action: "save_tg_rate", Target: tgDigits, Params: map[string]string{"batch": "90"}}); err != nil {
		t.Fatal(err)
	}
	if acc, _ = st.TGAccount(telegramAccount); acc.Every != 7 || acc.Batch != 90 {
		t.Errorf("непришедшее поле сбросилось: %d и %d", acc.Every, acc.Batch)
	}
}

// Плохое значение отвергается со словами и ничего не пишет — ни его, ни соседей
// по той же отправке.
func TestTelegramLimitFieldsRejectBadValues(t *testing.T) {
	s, st := telegramWithChannels(t, 0)
	if _, err := s.execute(actionRequest{Action: "save_tg_limits", Params: map[string]string{"reply_max_hour": "40"}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]string{
		{"reply_max_hour": "0"}, {"reply_max_hour": "много"}, {"reply_max_hour": ""}, {"wait_msgs": "-2"},
		{"stream_ms": "50"}, {"stream_first": "0"}, {"history": "-1"}, {"help_word": "   "},
		{"post_days": "30", "chats_max": "x"}, // вторая плохая — первая не пишется
	} {
		if _, err := s.execute(actionRequest{Action: "save_tg_limits", Params: bad}); err == nil {
			t.Errorf("%v принято", bad)
		}
	}
	if s.eng.TGReplyMaxHour() != 40 {
		t.Errorf("отвергнутые отправки что-то записали: %d", s.eng.TGReplyMaxHour())
	}
	if v := st.Setting("tg.post_days", "нет"); v != "нет" {
		t.Errorf("первая из двух полей записалась, хотя вторая плохая: %q", v)
	}
	for _, bad := range []map[string]string{{"every": "0"}, {"batch": "x"}, {"every": "5", "batch": "-1"}} {
		if _, err := s.execute(actionRequest{Action: "save_tg_rate", Target: tgDigits, Params: bad}); err == nil {
			t.Errorf("пределы обращений %v приняты", bad)
		}
	}
	if acc, _ := st.TGAccount(telegramAccount); acc.Every != 1 || acc.Batch != 50 {
		t.Errorf("отвергнутые пределы обращений что-то записали: %d и %d", acc.Every, acc.Batch)
	}
	if _, err := s.execute(actionRequest{Action: "save_tg_rate", Target: "70000000099", Params: map[string]string{"every": "5"}}); err == nil {
		t.Error("пределы чужой учётки приняты")
	}
}

// Прежние действия с вопросами работают как работали: на них держатся старые
// карточки и скрипты.
func TestTelegramOldLimitActionsStillWork(t *testing.T) {
	s, st := telegramWithChannels(t, 0)
	if _, err := s.execute(actionRequest{Action: "tg_set_limit", Target: "tg.reply_max_hour", Params: map[string]string{"value": "12"}}); err != nil {
		t.Fatal(err)
	}
	if s.eng.TGReplyMaxHour() != 12 {
		t.Errorf("старое действие не сохранило: %d", s.eng.TGReplyMaxHour())
	}
	if _, err := s.execute(actionRequest{Action: "tg_set_limit", Target: "tg.help_word", Params: map[string]string{"value": "бот"}}); err != nil {
		t.Fatal(err)
	}
	if s.eng.TGHelpWord() != "бот" {
		t.Errorf("слово-вызов: %q", s.eng.TGHelpWord())
	}
	if _, err := s.execute(actionRequest{Action: "tg_set_every", Target: tgDigits, Params: map[string]string{"value": "9"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute(actionRequest{Action: "tg_set_batch", Target: tgDigits, Params: map[string]string{"value": "99"}}); err != nil {
		t.Fatal(err)
	}
	if acc, _ := st.TGAccount(telegramAccount); acc.Every != 9 || acc.Batch != 99 {
		t.Errorf("старые пределы обращений: %d и %d", acc.Every, acc.Batch)
	}
}
