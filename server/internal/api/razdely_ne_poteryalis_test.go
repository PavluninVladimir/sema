package api

import (
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Карта переезда остальных разделов настроек: «Доступ», «Что ушло»,
// «Telegram», «Уведомления», «Серверы», «Хранилища», «Инструменты» и
// «Интеграции» разрезаны на внутренние вкладки, и ни одна настройка при
// этом не должна была исчезнуть. Список имён — снимок того, что было на
// одной ленте до разреза: строка отсюда пропала, значит настройка стала
// недостижимой, а человек этого сам не заметит.
//
// Проверяем имена полей и действий, а не подписи: подпись правят, а имя
// поля — это договор с сервером.

// screenSettings — что нашлось на всех вкладках раздела: имена полей и
// действий («!» перед действием, чтобы они не путались с полями). У
// действия с целью пишется ещё и «!действие:цель»: у пределов переписки
// и пределов SSH сама настройка сидит в цели, а имя действия у них одно
// на десяток строк — без цели пропажу одной строки не заметить.
func screenSettings(t *testing.T, s *Server, screens ...string) map[string]bool {
	t.Helper()
	has := map[string]bool{}
	for _, э := range screens {
		doc, err := s.buildScreen(э, "", "", 0)
		if err != nil {
			t.Fatalf("экран %s: %v", э, err)
		}
		var walk func([]Component)
		walk = func(cs []Component) {
			for _, c := range cs {
				for _, f := range c.Fields {
					has[f.Name] = true
				}
				запомнить := func(a Action) {
					has["!"+a.Action] = true
					if a.Target != "" {
						has["!"+a.Action+":"+a.Target] = true
					}
				}
				for _, a := range c.Actions {
					запомнить(a)
				}
				for _, a := range c.Menu {
					запомнить(a)
				}
				for _, sel := range c.Selects {
					запомнить(sel.Action)
				}
				// клетки матрицы (выдача секрета модулю) — те же действия, что были
				// кнопками строки
				for _, cell := range c.Cells {
					if cell.Action.Action != "" {
						запомнить(cell.Action)
					}
					// список в клетке матрицы (класс чата Telegram) — тоже действие
					if cell.Select != nil {
						запомнить(cell.Select.Action)
					}
				}
				if c.Submit != nil {
					has["!"+c.Submit.Action] = true
				}
				walk(c.Children)
				// ленивый раздел приезжает пустым — его содержимое тоже
				// достижимо, но по адресу части: обходим и его
				if c.Lazy != "" {
					часть, err := s.buildPart(э, c.Lazy, "", 0)
					if err != nil {
						t.Fatalf("экран %s, часть %s: %v", э, c.Lazy, err)
					}
					walk(часть)
				}
			}
		}
		walk(doc.Components)
		// кнопки шапки экрана — тоже действия («Завести ключ» на «Доступе» стоит
		// только там)
		for _, a := range append(append([]Action{}, doc.ToolbarActions...), doc.ToolbarMenu...) {
			has["!"+a.Action] = true
			if a.Target != "" {
				has["!"+a.Action+":"+a.Target] = true
			}
		}
	}
	return has
}

func checkSet(t *testing.T, section string, has map[string]bool, before []string) {
	t.Helper()
	for _, name := range before {
		if !has[name] {
			t.Errorf("%s: настройка %q не нашлась ни на одной вкладке — она стала недостижимой", section, name)
		}
	}
}

func TestAccessSettingsNotLost(t *testing.T) {
	s, st := testServer(t)
	if _, err := st.AddToken("проба", []string{"ui"}); err != nil {
		t.Fatal(err)
	}
	// ключ и открытая сессия: без них строк с «переименовать» и «выйти»
	// на экране нет вовсе, и проверять было бы нечего
	id, err := st.AddPasskey("мак", "проба-ключ", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddSession("проба-сессия", id, "Safari", "127.0.0.1", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	// вкладки: «Ключи», «Кто внутри», «Токены», «Доверенная сеть», «Кто стучался»
	// (2 октября 2026 ключи и сессии разъехались; до этого сессии жили на первой)
	has := screenSettings(t, s, accessScreen, accessScreen+"~"+accessTabInside, accessScreen+"~"+accessTabTokens,
		accessScreen+"~"+accessTabNetwork, accessScreen+"~"+accessTabLog)
	checkSet(t, "Доступ", has, []string{
		// доверенная сеть: «без проверки личности» сохраняется своей кнопкой,
		// «вся частная сеть своя» — тумблером строки с вопросом, остальное —
		// обычной
		auth.SettingLocalOpen, "!toggle_local_scope", auth.SettingLocalHosts,
		auth.SettingTrustProxy, auth.SettingUserVerification, auth.SettingRPID, auth.SettingOrigin,
		"!save_auth", "!save_auth_danger",
		// ключи и сессии
		"!rename_passkey", "!delete_passkey", "!logout_session", "!logout_all", "!$login",
		// токены
		"name", "scope_ui", "!add_token", "!revoke_token",
	})
}

// Опасное живёт своим блоком со своей кнопкой: общая полоса «Сохранить»
// не должна уносить переключатель, который выкидывает хозяина из Сёмы.
func TestDangerousSavedSeparately(t *testing.T) {
	s, _ := testServer(t)
	doc, err := s.buildScreen(accessScreen+"~"+accessTabNetwork, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var опасная, обычная *Component
	for i, c := range doc.Components {
		if c.Type != "form" {
			continue
		}
		switch c.ID {
		case "auth-danger":
			опасная = &doc.Components[i]
		case "auth-settings":
			обычная = &doc.Components[i]
		}
	}
	if опасная == nil || обычная == nil {
		t.Fatal("на «Доверенной сети» нет двух форм: обычной и опасной")
	}
	if опасная.Submit == nil || обычная.Submit == nil {
		t.Fatal("у формы нет своей кнопки")
	}
	if опасная.Submit.Action == обычная.Submit.Action {
		t.Errorf("обе формы шлют одно действие %q — опасное уедет вместе с именем сервера", опасная.Submit.Action)
	}
	for _, f := range обычная.Fields {
		if f.Name == auth.SettingLocalOpen || f.Name == auth.SettingLocalScope {
			t.Errorf("тумблер %q остался в общей форме", f.Name)
		}
	}
	// наверху вкладки — итог, а не пересказ тумблеров. Слово «вход»
	// из итога ушло нарочно: вход тут никто не проходит, в том и беда,
	// — ищем сам итог по его якорю
	dump := dumpScreen(doc)
	if !strings.Contains(dump, "сеть-итог") {
		t.Error("нет итога «кого пускает прямо сейчас»")
	}
	if !strings.Contains(dump, "без проверки личности") && !strings.Contains(dump, "Без входа не пускается никто") {
		t.Error("итог не называет, проверяется ли личность")
	}
}

func TestSentSettingsNotLost(t *testing.T) {
	s, _ := testServer(t)
	has := screenSettings(t, s, sentScreen, sentScreen+"~"+sentTabLog)
	checkSet(t, "Что ушло наружу", has, []string{
		"!toggle_sent_full", "!set_sent_keep", "!toggle_sensitive_ask", "!add_own_value",
	})
}

// Лента отправок из настроек выведена: в разделе остаются правила и
// хранение, итог одной строкой и ссылка на журнал. Сам журнал живёт
// своим экраном и открывается ещё и со «Здоровья».
func TestSentFeedMovedOutOfSettings(t *testing.T) {
	s, st := testServer(t)
	if err := st.AddSent(&store.SentRow{Consumer: "core:queue", Model: "qwen3", OK: true,
		Class: store.ClassPlain, TokensIn: 10, TokensOut: 5, Seconds: 1}); err != nil {
		t.Fatal(err)
	}
	настройки, err := s.buildScreen(sentScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range настройки.Components {
		if c.Type == "section" && c.Title == "Отправки" {
			t.Error("лента отправок так и осталась в настройках")
		}
	}
	if !strings.Contains(dumpScreen(настройки), sentScreen+"~"+sentTabLog) {
		t.Error("из настроек нет пути в журнал отправок")
	}
	журнал, err := s.buildScreen(sentScreen+"~"+sentTabLog, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dumpScreen(журнал), "Ядро · queue") {
		t.Error("в журнале нет самих отправок")
	}
}

func TestTelegramSettingsNotLost(t *testing.T) {
	s, st := testServer(t)
	// без входа вкладок нет вовсе: мастер из трёх шагов резать нельзя
	if err := s.eng.SaveTGCreds("12345", "хэш"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTGAccount(&store.TGAccount{Phone: telegramAccount, State: "ok", Enabled: true,
		Every: 1, Batch: 50}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTGChat(&store.TGChat{Account: telegramAccount, ID: 7, Title: "Собеседник", Kind: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTGChat(&store.TGChat{Account: telegramAccount, ID: 8, Title: "Канал", Kind: "channel"}); err != nil {
		t.Fatal(err)
	}
	has := screenSettings(t, s, tgScreen, tgScreen+"~"+tgTabAnswer, tgScreen+"~"+tgTabConn)
	checkSet(t, "Telegram", has, []string{
		// собеседники и их права — клетки матрицы: тот же `tg_chat_right`, что был
		// тумблером; семафор молчания, класс (список в клетке) и массовые кнопки
		"!tg_chat_right", "!tg_wait_toggle", "!tg_chat_class", "!tg_channels_all",
		// ответы и пределы: поля строками настройки вместо вопросов
		// (`tg_set_every`, `tg_set_batch` и `tg_set_limit` с вопросами остались
		// ради скриптов и старых карточек: TestTelegramOldLimitActionsStillWork)
		"!tg_toggle_send", "every", "batch", "!save_tg_rate",
		"help_word", "reply_max_hour", "wait_warn_min", "wait_alert_min", "wait_msgs",
		"history", "file_limit_mb", "post_days", "chats_max", "stream_ms", "stream_first", "!save_tg_limits",
		// разговор вживую: прочитано, печатает, ответ на глазах
		"!tg_toggle_flag:tg.read_mark", "!tg_toggle_flag:tg.read_mark_reply", "!tg_toggle_flag:tg.typing", "!tg_set_stream",
		// подключение
		"!tg_refresh_chats", "!tg_forget",
	})
}

func TestNotificationsSettingsNotLost(t *testing.T) {
	s, st := testServer(t)
	if err := st.SaveNotifyRule(&store.NotifyRule{Title: "Счета", Cond: "words:счёт",
		Kind: "due", Days: 3, Mode: "now", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// устройство с пушами: без него у раскрытия строки нечего проверять
	subscribeAs(t, s, androidUA, "https://fcm.googleapis.com/fcm/send/A1", false, 5)
	has := screenSettings(t, s, notifySettingsScreen,
		notifySettingsScreen+"~"+notifyTabRules, notifySettingsScreen+"~"+notifyTabShow,
		entityAddr(notifySettingsScreen, notifyNew, ""))
	checkSet(t, "Уведомления", has, []string{
		// каналы доставки; «Добавить канал» — свой экран
		"kind", "title", "config", "!add_notify_channel", "!toggle_notify_channel",
		"!test_notify_channel", "!delete_notify_channel", "!$push",
		// порог, источники и тихие часы канала — поля раскрытия строки вместо вопросов
		// и списка в строке (`notify_channel_level`, `…_quiet`, `…_sources` и
		// `…_title` остались ради скриптов и старых карточек:
		// TestChannelActionsWithoutScreenStillWork)
		"level", "sources", "quiet", "!save_notify_channel",
		// устройства с пушами: имя, порог, тихие часы — поля раскрытия (`push_rename`,
		// `push_level`, `push_quiet` — TestPushDeviceActions)
		"quiet_mode", "!save_push_device", "!push_test", "!push_unsubscribe",
		// параметры пушей — «Тонкая настройка»
		"subject", "ttl_min", "ttl_alert_min", "sticky_alert", "urgency_alert", "urgency_warn", "urgency_info", "!set_push_settings",
		// что показывать: провод, опрос, чаты команд и пояс отсюда переехали на
		// «Работу Сёмы» (TestWorkSettingsNotLost, TestMovedSettingsLiveInOnePlace)
		"counter", "keep", "snooze", "!set_notify_settings",
		// правила почты
		"cond", "days", "!add_notify_rule", "!toggle_notify_rule", "!notify_rule_days", "!delete_notify_rule",
	})
}

func TestHostsSettingsNotLost(t *testing.T) {
	s, st := testServer(t)
	// форма добавления показывается, только когда в хранилище есть ключ
	if err := st.SaveSecret(&store.Secret{Name: "ssh:проба", Kind: store.SecretSSH}, "ключ"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveHost(&store.Host{Name: "проба", Addr: "10.0.0.1", Port: 22,
		User: "root", Key: "ssh:проба"}); err != nil {
		t.Fatal(err)
	}
	// правила у каждого сервера свои: без них колонки пусты, и убирать нечего
	if err := st.AddHostRule("проба", store.RuleAllow, "uptime"); err != nil {
		t.Fatal(err)
	}
	// у каждой машины свой экран (1 октября 2026): список, общая
	// «Самостоятельность», общий журнал, «Добавить сервер» и четыре вкладки
	// самого сервера
	has := screenSettings(t, s, serversScreen, serversScreen+"~"+serversTabAuto, serversScreen+"~"+serversTabLog,
		entityAddr(serversScreen, serversNew, ""),
		entityAddr(serversScreen, "проба", ""), entityAddr(serversScreen, "проба", serversTabAuto),
		entityAddr(serversScreen, "проба", serversTabRules), entityAddr(serversScreen, "проба", serversTabLog))
	checkSet(t, "Серверы", has, []string{
		// машины и добавление
		"name", "addr", "port", "user", "sudo", "key", "!add_host", "!check_host",
		"!toggle_host_sudo", "!delete_host", "!add_home_host",
		// стоп-кран
		"!stop_all_auto",
		// правила сервера и пределы
		"cmd", "!add_host_rule", "!delete_host_rule", "!take_rule_set", "!check_host_rule",
		// то, что правилось вопросом или списком в строке, правится полями экрана
		// сервера: род — полем «kind», уровень, окно и потолок — полями «level»,
		// «window» и «max_hour» одной формы; сами действия остались ради
		// уведомлений и скриптов (TestHostActionsWithoutScreenStillWork)
		"kind", "!save_host", "level", "window", "!set_host_autonomy",
		// пределы SSH, страховка и ночная смена — «Тонкая настройка» вкладки
		// «Самостоятельность»: три предела SSH переехали с «Правил», остальные
		// восемь до этого не правились с экрана вовсе
		"timeout_s", "confirm_timeout_s", "max_output", "!set_ssh_limits",
		"max_steps", "look_max", "look_timeout_s", "every_h", "skill_linux", "skill_routeros", "!set_night_limits",
		"rollback_min", "max_hour", "!set_auto_limits",
	})
}

func TestStoragesSettingsNotLost(t *testing.T) {
	s, st := testServer(t)
	if err := st.AddFileFolder("/data/inbox", false); err != nil {
		t.Fatal(err)
	}
	// у каждого хранилища свой экран, а «Добавить хранилище» — свой (S3 и Диск —
	// вкладки): заводим по одному каждого вида, чтобы у экранов были поля
	if err := st.AddStorage("s3-проба", "s3", `{"endpoint":"s3.example.org","bucket":"b","ssl":true,"access_secret":"k1","secret_secret":"k2"}`); err != nil {
		t.Fatal(err)
	}
	if err := st.AddStorage("диск-проба", "yadisk", `{"token_secret":"disk-token","folder":"Сёма"}`); err != nil {
		t.Fatal(err)
	}
	has := screenSettings(t, s, storagesScreen, storagesScreen+"~"+storTabSources,
		entityAddr(storagesScreen, storNew, ""), entityAddr(storagesScreen, storNew, storNewDisk),
		storageAddr("s3-проба"), storageAddr("диск-проба"), storageAddr("local"))
	checkSet(t, "Хранилища", has, []string{
		// где лежат: добавление S3 и Диска — свой экран, параметры — поля экрана хранилища
		"name", "preset", "endpoint", "bucket", "region", "prefix", "ssl", "access_secret", "secret_secret",
		"token_secret", "folder", "!add_storage", "!add_yadisk", "!check_storage", "!delete_storage", "!save_storage",
		// откуда берутся: «удалять после приёма» — поле раскрытия папки, а не кнопка
		// (`toggle_folder_delete` остался: TestFolderActionsWithoutScreenStillWork)
		"!toggle_files_upload", "!toggle_offload", "!toggle_mirror", "!toggle_backup",
		"!toggle_files_parse", "!toggle_folder_source", "!save_folder",
		"!delete_folder_source", "path", "delete_after", "!add_folder_source",
	})
}

func TestToolsSettingsNotLost(t *testing.T) {
	s, _ := testServer(t)
	// смена ключа («!rotate_key») с «Инструментов» переехала на вкладку
	// «Шифрование» «Ключей и секретов»: её держит TestSecretsAndAgentsSettingsNotLost
	has := screenSettings(t, s, toolsScreen, toolsScreen+"~"+toolsTabCodes)
	checkSet(t, "Инструменты", has, []string{
		"!toggle_chat_scripts", "!toggle_code_kind",
		// пределы кодов — поля «Тонкой настройки» (прежние `save_codes_limit` и
		// `save_codes_pixels` со списками остались: TestCodesSettingsOnTools)
		"limit_s", "max_mp", "!set_codes_limits",
	})
}

func TestIntegrationsSettingsNotLost(t *testing.T) {
	s, _ := mcpScreensFix(t)
	// вход на «wiki» начат: появляются «Открыть страницу входа» и «Отменить вход»
	if _, err := s.eng.MCPLoginBegin(t.Context(), "wiki", "https://sema.local:8443", "session:1"); err != nil {
		t.Fatal(err)
	}
	has := screenSettings(t, s, mcpScreen, entityAddr(mcpScreen, mcpNew, ""),
		entityAddr(mcpScreen, "sing", ""), entityAddr(mcpScreen, "sing", mcpTabRules), entityAddr(mcpScreen, "sing", mcpTabLog),
		entityAddr(mcpScreen, "ключ", ""), entityAddr(mcpScreen, "код", ""), entityAddr(mcpScreen, "wiki", ""))
	checkSet(t, "Интеграции", has, []string{
		// «Добавить сервер MCP»: все десять полей прежнего заведения
		"name", "url", "agent", "can", "secret", "header", "prefix", "login_scope", "about", "keywords", "!add_mcp",
		// «Тонкая настройка» раздела: пятнадцать пределов и поля выжимки списка
		"chat_chars", "chat_calls", "chat_result_chars", "chat_list_fields", "card_chars", "recheck_s", "error_chars", "arg_retries", "tool_hint_days", "tool_hint_chars",
		"login_ttl_min", "refresh_skew_s", "net_timeout_s", "token_retries", "token_retry_ms", "client_metadata",
		"!set_mcp_chat_limits", "!set_mcp_login_limits",
		// экран сервера: «В чате», права входа, вход, подсказки, «Опасное»
		"chat", "about", "keywords", "class", "!save_mcp_chat", "login_scope", "!save_mcp_login_scope",
		"!mcp_login", "!mcp_login_cancel", "!$open", "!mcp_logout", "!toggle_mcp", "!check_mcp", "!delete_mcp",
		"!forget_mcp_hints", "!forget_mcp_hint",
		// правила: ответ для нового, группы, названия групп
		"default", "bulk", "tool:task_list", "groups", "!save_mcp_rules", "!save_mcp_groups",
		// вызовы на подтверждение
		"!approve_mcp", "!reject_mcp",
	})
}

// Внутренняя вкладка — адрес: по нему возвращаются, и выбранный раздел в
// колонке при этом не сбивается.
func TestSectionSubtabsAreAddressable(t *testing.T) {
	s, _ := testServer(t)
	для := map[string]string{
		accessScreen + "~" + accessTabNetwork:       accessScreen,
		accessScreen + "~" + accessTabInside:        accessScreen,
		accessScreen + "~" + accessTabLog:           accessScreen,
		notifySettingsScreen + "~" + notifyTabRules: notifySettingsScreen,
		serversScreen + "~" + serversTabAuto:        serversScreen,
		serversScreen + "~" + serversTabLog:         serversScreen,
		"settings:storages~" + storTabSources:       "settings:storages",
		// «Шифрование» переехало с «Инструментов» к «Ключам и секретам»
		secretsScreen + "~" + secretsTabCrypt: secretsScreen,
		secretsScreen + "~" + secretsTabLog:   secretsScreen,
		agentsScreen + "~" + agentsTabGateway: agentsScreen,
	}
	for screen, section := range для {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		if doc.ActivePanel != section {
			t.Errorf("%s: в списке разделов выбран %q", screen, doc.ActivePanel)
		}
		if doc.ActiveSubtab != screen {
			t.Errorf("%s: выбранная внутренняя вкладка %q", screen, doc.ActiveSubtab)
		}
		if len(doc.Tabs) != 0 {
			t.Errorf("%s: у настроек вкладок быть не должно, а их %d", screen, len(doc.Tabs))
		}
	}
}
