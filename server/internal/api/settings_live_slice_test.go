package api

// «Живой срез» журнала настроек: ключи и значения такие, какие 2 октября 2026
// лежали на живой базе под «Вашими правками» и в «Вы меняли» — сырые ключи вместо
// имён. Служебные отметки, которые машина пишет в долю секунды действия человека
// (решения по картинкам, счётчики чатов MAX и Telegram, привязки), составные
// настройки одной строкой JSON (`models.json`…) и одиночные ключи, у которых не
// было ни реестра, ни имени (`images.mode`, `night.dry_run`…). Значения придуманы;
// проверяется то, что человек увидит.

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// liveEdit — одна подписанная правка: что записано и каким действием.
type liveEdit struct {
	key, was, now string
	action        string // действие, которым правка сделана
	screen        string // экран, с которого нажали; пусто — найдётся по действию
}

// liveService — служебное, подписанное человеком: должно уйти в «Служебные».
var liveService = []liveEdit{
	{key: "vision.decision.c89bc0c31e22be93eb10043e5218e19b22d6ca6a1ba5e7c2f8e5ee3d7ae7d3f0", now: "ok", action: "decide_image"},
	{key: "vision.decision.ea8b09ab5aa134ac5fb28ca475b675bf44a1c0e4f9e3a8d5a6b7c8d9e0f1a2b3", now: "skip", action: "decide_image"},
	{key: "vision_state", now: `{"v":3}`, action: "decide_image"},
	{key: "max.chat.-71234567890", now: "1", action: "max_open_chat"},
	{key: "max.history.-71234567890", now: "100", action: "max_open_chat"},
	{key: "tg.chat.79990000000.55", now: "1", action: "tg_open_chat"},
	{key: "tg.quiet.79990000000:55", now: "1", action: "tg_open_chat"},
	{key: "tg.refs.79990000000", now: "40", action: "tg_open_chat"},
	{key: "chat.nudges.71", now: "2", action: "send_message"},
	{key: "role.fails.Чаты", now: "2", action: "chat_reply"},
	{key: "watch.doctor.last.cert", now: "2026-10-01", action: "toggle_watch_doctor"},
	{key: "ssh.recard.srv-vpn", now: "1", action: "approve_ssh"},
	{key: "providers.seen.Perplexity", now: "1", action: "connect_provider"},
	{key: "images.cheapest", now: `{"endpoint":"fal-ai/flux-2"}`, action: "save_image_mode"},
	{key: "memory.suggestions.json", now: `["a"]`, action: "memory_suggest"},
	{key: "archive_folder", was: "Archive", now: "Архив", action: "save_mail"},
	{key: "models_removed", now: "gpt-4o", action: "remove_model"},
}

// liveHuman — правки человека: у каждой должно быть имя, а не ключ.
func liveHuman() []liveEdit {
	return []liveEdit{
		{key: "images.mode", was: "cheap", now: "better", action: "save_image_mode"},
		{key: "ocr_engine", was: "vision", now: "tesseract", action: "save_ocr_engine"},
		{key: "watch.doctor", was: "1", now: "0", action: "toggle_watch_doctor"},
		{key: "night.dry_run", was: "1", now: "0", action: "toggle_night_dry"},
		{key: "web.find_url", was: "", now: "http://searxng:8080", action: "set_web_find"},
		{key: "auth.user_verification", was: "preferred", now: "required", action: "save_auth"},
		{key: "modules.task_timeout_s", was: "600", now: "900", action: "set_module_task_timeout", screen: "modules"},
		{key: "scripts.timeout_s", was: "300", now: "400", action: "set_module_task_timeout", screen: "modules"},
		{key: "models.json", was: `[{"name":"gpt-6-luna","price":"$0.55 / 1M"},{"name":"qwen3"}]`,
			now: `[{"name":"gpt-6-luna","price":"$0.60 / 1M"},{"name":"qwen3"}]`, action: "set_price"},
		{key: "roles.json", was: `[{"name":"Чаты","model":"gpt-6-luna","spare":"нет"}]`,
			now: `[{"name":"Чаты","model":"DeepseekPro","spare":"нет"}]`, action: "set_role_model"},
		{key: "modules.json", was: `{"receipts":{"name":"receipts","title":"Чеки","enabled":false}}`,
			now: `{"receipts":{"name":"receipts","title":"Чеки","enabled":true}}`, action: "toggle_module"},
		{key: "mailconf.json", was: `{"me@example.com":{"junk_days":14}}`,
			now: `{"me@example.com":{"junk_days":30}}`, action: "save_mail"},
		{key: "providers.json", was: `[{"name":"Perplexity","need_key":true}]`,
			now: `[{"name":"Perplexity","need_key":true},{"name":"OpenAI","need_key":true}]`, action: "connect_provider"},
	}
}

// applyLive — записать правки подписанными, как это делает `executeSigned`.
func applyLive(t *testing.T, s *Server, st *store.Store, list []liveEdit) {
	t.Helper()
	idx := s.settingsIndex()
	for _, ed := range list {
		screen := ed.screen
		if screen == "" {
			if es := idx.byAction[ed.action]; len(es) > 0 {
				screen = es[0].Addr
			}
		}
		if ed.was != "" {
			_ = st.SetSetting(ed.key, ed.was)
		}
		mark := st.LastChange()
		if err := st.SetSetting(ed.key, ed.now); err != nil {
			t.Fatal(err)
		}
		from := ed.action
		if screen != "" {
			from = screen + " · " + ed.action
		}
		st.SignChanges(mark, "вы", from)
	}
}

// Под «Ваши правки» нет ни одной записи с сырым ключом вместо имени; служебное,
// подписанное человеком, ушло в «Служебные», и ничего не удалено.
func TestLiveSliceHasNamesNotKeys(t *testing.T) {
	s, st := indexFix(t, 20)
	// журнал фикстуры чистый: считаем только то, что добавит срез
	_, _, totalBefore := st.CountSettingsLogView()
	applyLive(t, s, st, liveService)
	applyLive(t, s, st, liveHuman())

	mine, err := st.SettingsLogView(store.SettingsLogMine, 500)
	if err != nil {
		t.Fatal(err)
	}
	idx := s.settingsIndex()
	named := map[string]string{}
	for _, chg := range mine {
		name := idx.nameOf(chg)
		named[chg.Key] = name.Title
		if name.Title == chg.Key {
			t.Errorf("«Ваши правки»: %s без имени", chg.Key)
		}
		if strings.Contains(name.Title, ".json") {
			t.Errorf("%s: в названии остался ключ: %q", chg.Key, name.Title)
		}
	}
	for _, ed := range liveHuman() {
		if _, ok := named[ed.key]; !ok {
			t.Errorf("правка %s пропала из «Ваших»", ed.key)
		}
	}
	for _, ed := range liveService {
		if _, ok := named[ed.key]; ok {
			t.Errorf("служебное %s попало в «Ваши правки»", ed.key)
		}
	}

	// все служебные лежат в «Служебных»
	service, _ := st.SettingsLogView(store.SettingsLogService, 1000)
	have := map[string]bool{}
	for _, chg := range service {
		have[chg.Key] = true
	}
	for _, ed := range liveService {
		if !have[ed.key] {
			t.Errorf("служебное %s не нашлось среди служебных", ed.key)
		}
	}
	// ничего не удалено: отборы дают весь журнал, и в нём не меньше, чем записали
	nMine, nService, nTotal := st.CountSettingsLogView()
	if nMine+nService != nTotal || nTotal < totalBefore+len(liveService)+len(liveHuman()) {
		t.Errorf("записи потерялись: %d + %d, всего %d (было %d)", nMine, nService, nTotal, totalBefore)
	}

	// на экране журнала и на Обзоре сырого ключа в заголовках нет
	for _, screen := range []string{changesScreen, settingsOverview} {
		doc, err := s.buildScreen(screen, "", "", 400)
		if err != nil {
			t.Fatal(err)
		}
		var walk func(cs []Component)
		walk = func(cs []Component) {
			for _, c := range cs {
				if strings.HasPrefix(c.ID, "правка-") {
					if c.Subtitle == "" || strings.ContainsAny(c.Subtitle, "_") && !strings.Contains(c.Subtitle, " ") {
						t.Errorf("%s: заголовок правки — ключ: %q", screen, c.Subtitle)
					}
					for _, ed := range liveService {
						if c.Subtitle == ed.key {
							t.Errorf("%s: служебное %s на экране «Ваших»", screen, ed.key)
						}
					}
				}
				walk(c.Children)
			}
		}
		walk(doc.Components)
	}
}

// «Было → стало» у правок срез показывает словами, а значения секретов не пишет.
func TestLiveSliceShowsShortValues(t *testing.T) {
	s, st := indexFix(t, 20)
	applyLive(t, s, st, liveHuman())
	mine, _ := st.SettingsLogView(store.SettingsLogMine, 500)
	idx := s.settingsIndex()
	want := map[string][2]string{
		"night.dry_run":          {"выключено", "включено"}, // тумблер читается наоборот настройке
		"watch.doctor":           {"включено", "выключено"},
		"auth.user_verification": {"выключено", "включено"},
		"modules.task_timeout_s": {"600 с", "900 с"},
		"web.find_url":           {"не задано", "http://searxng:8080"},
	}
	for _, chg := range mine {
		row := s.namedEditRow(idx, chg, false)
		if w, ok := want[chg.Key]; ok {
			got := [2]string{}
			for _, f := range row.Facts {
				switch f.Label {
				case "Было":
					got[0] = f.Value
				case "Стало":
					got[1] = f.Value
				}
			}
			if got != w {
				t.Errorf("%s: было → стало %v, ждали %v", chg.Key, got, w)
			}
		}
		for _, f := range row.Facts {
			if len([]rune(f.Value)) > 80 || strings.ContainsAny(f.Value, "{}[]") && chg.Note != nil {
				t.Errorf("%s: длинное или сырое значение: %q", chg.Key, f.Value)
			}
		}
	}
}

// Составная настройка в журнале: слова вместо ключа, «было → стало» только у того,
// что можно показать, переход — к сущности, если она одна, иначе к разделу.
func TestLiveSliceCompositeRows(t *testing.T) {
	s, st := indexFix(t, 20)
	applyLive(t, s, st, liveHuman())
	mine, _ := st.SettingsLogView(store.SettingsLogMine, 500)
	idx := s.settingsIndex()
	type want struct{ title, was, now, goto_ string }
	wants := map[string]want{
		"models.json":    {"Модели: у gpt-6-luna изменена цена", "$0.55 / 1M", "$0.60 / 1M", modelAddr("gpt-6-luna")},
		"roles.json":     {"Роли: «Чаты» — модель DeepseekPro вместо gpt-6-luna", "gpt-6-luna", "DeepseekPro", modelsScreen},
		"modules.json":   {"Модули: «Чеки» включён", "", "", "modules"},
		"mailconf.json":  {"Почта: ящик me@example.com — срок спама 30", "14", "30", entityAddr(mailScreen, "me@example.com", "")},
		"providers.json": {"Провайдеры: подключён провайдер OpenAI", "", "", providerAddr("OpenAI", providerTabKey)},
	}
	seen := 0
	for _, chg := range mine {
		w, ok := wants[chg.Key]
		if !ok {
			continue
		}
		seen++
		row := s.namedEditRow(idx, chg, false)
		if row.Subtitle != w.title {
			t.Errorf("%s: название %q, ждали %q", chg.Key, row.Subtitle, w.title)
		}
		var was, now string
		for _, f := range row.Facts {
			switch f.Label {
			case "Было":
				was = f.Value
			case "Стало":
				now = f.Value
			}
		}
		if was != w.was || now != w.now {
			t.Errorf("%s: было → стало %q → %q, ждали %q → %q", chg.Key, was, now, w.was, w.now)
		}
		if w.was == "" && len(row.Facts) != 0 {
			t.Errorf("%s: пустые «Было / Стало» вместо отсутствия: %+v", chg.Key, row.Facts)
		}
		var target string
		for _, a := range row.Actions {
			if a.Action == "$goto" {
				target = a.Target
			}
		}
		if target != w.goto_ {
			t.Errorf("%s: переход %q, ждали %q", chg.Key, target, w.goto_)
		}
		if strings.Contains(row.Title, "..") || !strings.Contains(row.Title, " → ") && row.Title != "Модули" {
			t.Errorf("%s: путь %q", chg.Key, row.Title)
		}
	}
	if seen != len(wants) {
		t.Errorf("составных правок в «Ваших» %d, ждали %d", seen, len(wants))
	}
}

// Разведка: параметры, которые человек правит с экрана, а имени по ключу у журнала
// нет. Ничего не проверяет — печатается с -v и идёт в отчёт.
func TestZZLiveUnnamedKeys(t *testing.T) {
	s, _ := indexFix(t, 20)
	idx := s.settingsIndex()
	keys := []string{"agents.gateway", "agents.gateway_upstream", "agents.listen", "app.timezone", "audit.keep_days", "audit.keep_full",
		"audit.sensitive", "auth.local_open", "auth.local_scope", "auth.trust_proxy", "auth.user_verification", "backup.on", "chat_scripts",
		"codes.kinds", "codes.max_mp", "codes.max_s", "digest.collapse_from", "digest.window", "embed.trusted", "embed_base_url", "embed_model",
		"files.offload", "files.parse", "files.upload", "images.mode", "junk.retention_days", "learn.judge", "learn.on", "llm.active",
		"mail.keep_days", "mail.money_guard", "mail.money_words", "mail.purge_other", "mail.read_server_folders", "mail.send_auto_to",
		"mail.send_mode", "mail.translate", "mail.translate_chars", "mail.translate_to", "mail.whitelist_only", "max.allow_expired_cert",
		"memory.review", "mirror.on", "modules.task_timeout_s", "night.dry_run", "notify.counter_level", "notify.keep_days", "notify.snooze",
		"ocr_engine", "payments.on", "payments.receipt_wait_days", "prices.skip_sections", "push.sticky_alert", "push.subject",
		"queue.cloud_after", "queue.cloud_help", "queue.max_load", "queue.paused", "rag.auto_threshold", "scripts.timeout_s",
		"search.save_pages", "settings.log_keep_days", "spam.server_votes", "speech.files", "speech.language", "speech.outside",
		"tg.stream", "update.url", "vendors.forms", "vision.no_text", "watch.disagree", "watch.keep_days", "watch.on", "watch.repeat",
		"watch.stop", "watch.window_h", "web.find_url"}
	var unnamed []string
	for _, k := range keys {
		if idx.nameOf(store.SettingChange{Key: k}).Title == k {
			unnamed = append(unnamed, k)
		}
	}
	t.Logf("без имени по ключу (%d из %d): %s", len(unnamed), len(keys), strings.Join(unnamed, ", "))
}
