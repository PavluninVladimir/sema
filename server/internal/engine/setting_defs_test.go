package engine

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Умолчание каждой настройки реестра читает сам движок: на пустой базе
// читатель отдаёт то, что в реестре, а экран показывает ровно это же. Тест
// перебирает **все** описания реестра, а не одно: настройка, заведённая в
// реестре без строки здесь, роняет его, и ей придётся показать, чем её читает код.
//
// Читатель — функция движка, а не само описание: проверка, что реестр равен
// реестру, ничего бы не стоила.
func TestEveryRegisteredSettingIsReadByEngineWithItsDefault(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	h := &store.Host{Name: "проба", Kind: store.HostLinux}
	limits := e.MCPChatLimits()
	ttl, skew, netTimeout := e.MCPLoginLimits()
	tries, pause := e.MCPRetryLimits()
	readers := map[string]func() string{
		"mcp.chat_chars":        func() string { return strconv.Itoa(limits.Chars) },
		"mcp.chat_calls":        func() string { return strconv.Itoa(limits.Calls) },
		"mcp.chat_result_chars": func() string { return strconv.Itoa(limits.Result) },
		"mcp.card_chars":        func() string { return strconv.Itoa(limits.Card) },
		"mcp.recheck_s":         func() string { return strconv.Itoa(limits.Recheck) },
		"mcp.error_chars":       func() string { return strconv.Itoa(limits.ErrorChars) },
		"mcp.arg_retries":       func() string { return strconv.Itoa(limits.Retries) },
		"mcp.tool_hint_days":    func() string { return strconv.Itoa(limits.HintDays) },
		"mcp.tool_hint_chars":   func() string { return strconv.Itoa(limits.HintChars) },
		"mcp.login_ttl_min":     func() string { return strconv.Itoa(int(ttl / time.Minute)) },
		"mcp.refresh_skew_s":    func() string { return strconv.Itoa(int(skew / time.Second)) },
		"mcp.net_timeout_s":     func() string { return strconv.Itoa(int(netTimeout / time.Second)) },
		"mcp.token_retries":     func() string { return strconv.Itoa(tries) },
		"mcp.token_retry_ms":    func() string { return strconv.Itoa(int(pause / time.Millisecond)) },
		"mcp.client_metadata":   func() string { return flagString(e.MCPClientMetadataOn()) },

		"chats.read_links":       func() string { return strconv.Itoa(e.ReadLinksCount()) },
		"search.save_pages":      func() string { return strconv.Itoa(e.SavePagesCount()) },
		"chats.read_links_chars": func() string { return strconv.Itoa(e.ReadLinksChars()) },
		"chats.read_links_ttl_h": func() string { return strconv.Itoa(e.ReadLinksTTLHours()) },
		"web.page_chars":         func() string { return strconv.Itoa(e.WebPageChars()) },
		"web.page_links":         func() string { return strconv.Itoa(e.WebPageLinks()) },
		"web.page_min_chars":     func() string { return strconv.Itoa(e.WebPageMinChars()) },
		"web.find_per_day":       func() string { return strconv.Itoa(e.WebFindPerDay()) },
		"web.find_gap_ms":        func() string { return strconv.Itoa(e.WebFindGapMS()) },
		"web.find_cache_min":     func() string { return strconv.Itoa(e.WebFindCacheMin()) },

		"night.max_steps":       func() string { return strconv.Itoa(e.NightMaxSteps()) },
		"night.look_max":        func() string { return strconv.Itoa(nightLookMaxSeen(t, e, st)) },
		"night.look_timeout_s":  func() string { return strconv.Itoa(st.SettingInt(nightLookTimeoutDef)) },
		"night.every_h":         func() string { return strconv.Itoa(nightEveryHoursSeen(t, e)) },
		"night.skill.linux":     func() string { return e.nightSkillName(store.HostLinux) },
		"night.skill.routeros":  func() string { return e.nightSkillName(store.HostRouterOS) },
		"auto.rollback_min":     func() string { return strconv.Itoa(e.InsuranceMinutes()) },
		"auto.max_hour":         func() string { return strconv.Itoa(e.AutoMaxHour(h)) },
		"ssh.timeout_s":         func() string { return strconv.Itoa(e.SSHTimeout()) },
		"ssh.confirm_timeout_s": func() string { return strconv.Itoa(e.SSHConfirmTimeout()) },
		"ssh.max_output":        func() string { return strconv.Itoa(e.SSHMaxOutput()) },

		// модели и речь: читает движок, умолчание — то же, что на экране
		"models.max_output":    func() string { return strconv.Itoa(e.maxOutTokens()) },
		"prices.skip_sections": func() string { return e.PriceSkipSections() },
		"speech.outside":       func() string { return e.SpeechOutside() },
		"speech.language":      func() string { return e.SpeechLanguage() },
		"speech.files":         func() string { return e.SpeechFiles() },

		// почта, «Тонкая настройка» вкладки «Спам»
		"spam.server_votes": func() string { return strconv.Itoa(e.ServerSpamVotes()) },
		"mail.money_words":  func() string { return strings.Join(e.MoneyWords(), ", ") },

		// пуши на устройства: адрес для связи пуст, пока человек его не вписал, —
		// тогда ядро берёт адрес входа или запасной, и запасной «настоящим» не
		// считается
		"push.subject": func() string {
			if _, real := e.PushSubject(); !real {
				return ""
			}
			return "настоящий"
		},
		"push.ttl_min":       func() string { return strconv.Itoa(e.pushTTL("info") / 60) },
		"push.ttl_alert_min": func() string { return strconv.Itoa(e.pushTTL("alert") / 60) },
		"push.sticky_alert":  func() string { return flagString(e.PushStickyAlert()) },
		"push.urgency.alert": func() string { return string(e.pushUrgency("alert")) },
		"push.urgency.warn":  func() string { return string(e.pushUrgency("warn")) },
		"push.urgency.info":  func() string { return string(e.pushUrgency("info")) },
		// коды на картинках
		"codes.max_s":  func() string { return strconv.Itoa(int(e.CodesLimit() / time.Second)) },
		"codes.max_mp": func() string { return strconv.Itoa(e.PixelsLimit()) },

		// Telegram, «Ответы и пределы»
		"tg.reply_max_hour": func() string { return strconv.Itoa(e.TGReplyMaxHour()) },
		"tg.history":        func() string { return strconv.Itoa(e.TGHistoryLimit()) },
		"tg.file_limit_mb":  func() string { return strconv.Itoa(e.TGFileLimitMB()) },
		"tg.post_days":      func() string { return strconv.Itoa(e.tgPostDays()) },
		"tg.chats_max":      func() string { return strconv.Itoa(e.TGChatsMax()) },
		"tg.wait_warn_min":  func() string { warn, _, _ := e.tgWaitLimits(); return strconv.Itoa(int(warn / time.Minute)) },
		"tg.wait_alert_min": func() string { _, alert, _ := e.tgWaitLimits(); return strconv.Itoa(int(alert / time.Minute)) },
		"tg.wait_msgs":      func() string { _, _, n := e.tgWaitLimits(); return strconv.Itoa(n) },
		"tg.help_word":      func() string { return e.TGHelpWord() },
		"tg.stream_ms":      func() string { return strconv.Itoa(int(e.tgStreamEvery() / time.Millisecond)) },
		"tg.stream_first":   func() string { return strconv.Itoa(e.tgStreamFirst()) },

		// «Работа Сёмы» (2 октября 2026): провод и опрос экрана, чаты команд, ветки
		"ui.live_ms":            func() string { return strconv.Itoa(int(e.LiveSettings().Window / time.Millisecond)) },
		"ui.poll_s":             func() string { return strconv.Itoa(e.LiveSettings().PollS) },
		"ui.poll_live_s":        func() string { return strconv.Itoa(e.LiveSettings().PollLiveS) },
		"ui.ws_write_s":         func() string { return strconv.Itoa(int(e.LiveSettings().WriteWait / time.Second)) },
		"ui.ws_per_session":     func() string { return strconv.Itoa(e.LiveSettings().PerSession) },
		"ui.snapshot_days":      func() string { return strconv.Itoa(e.LiveSettings().SnapshotDays) },
		"debug.pprof":           func() string { return flagString(e.PprofOn()) },
		"ui.list_rows":          func() string { return strconv.Itoa(e.ListRows()) },
		"ui.prefetch_ms":        func() string { return strconv.Itoa(e.PrefetchMs()) },
		"debug.slow_sql_ms":     func() string { return strconv.Itoa(e.SlowSQLMs()) },
		"http.redirect_tls":     func() string { return flagString(e.RedirectTLS()) },
		"chats.command_ttl_h":   func() string { return strconv.Itoa(e.CommandChatTTL()) },
		"chats.nudge_after_min": func() string { return strconv.Itoa(e.NudgeAfterMin()) },
		"chats.nudge_max":       func() string { return strconv.Itoa(e.NudgeMax()) },
		"chats.thread_tail":     func() string { return strconv.Itoa(e.ThreadTail()) },
		"chats.read_steps":      func() string { return strconv.Itoa(e.chatReadSteps()) },
		"chats.promise_markers": func() string { return strings.Join(e.promiseMarkers(), ", ") },
		"chats.claim_markers":   func() string { return strings.Join(e.claimMarkers(), ", ") },
		"mcp.chat_list_fields":  func() string { return strings.Join(e.mcpListFields(), ", ") },
		"share.days":            func() string { return strconv.Itoa(e.shareDays()) },
		// поручения (assign.go)
		"assign.confirm":             func() string { return flagString(e.AssignLimits().Confirm) },
		"assign.max_steps":           func() string { return strconv.Itoa(e.AssignLimits().MaxSteps) },
		"assign.step_turns":          func() string { return strconv.Itoa(e.AssignLimits().StepTurns) },
		"assign.days":                func() string { return strconv.Itoa(e.AssignLimits().Days) },
		"assign.tracker":             func() string { return e.AssignLimits().Tracker },
		"assign.tracker_project":     func() string { return e.AssignLimits().TrackerProj },
		"assign.tracker_create_tool": func() string { return e.AssignLimits().CreateTool },
		"assign.tracker_done_tool":   func() string { return e.AssignLimits().DoneTool },
		"assign.tracker_cancel_tool": func() string { return e.AssignLimits().CancelTool },

		"skills.max_tokens":       func() string { return strconv.Itoa(e.SkillsMaxTokens()) },
		"learn.max_per_day":       func() string { return strconv.Itoa(e.LearnMaxPerDay()) },
		"learn.min_score":         func() string { return strconv.Itoa(e.LearnMinScore()) },
		"agents.listen":           func() string { return e.AgentListen() },
		"agents.gateway":          func() string { return e.GatewayAddr() },
		"agents.gateway_upstream": func() string { return st.Setting("agents.gateway_upstream", "") },
		SettingsLogKeepKey:        func() string { return strconv.Itoa(e.SettingsLogKeepDays()) },
		// служебные отметки журнала настроек: читает хранилище (журнал глазами человека),
		// пустая настройка — первичное заполнение
		"settings_log.marks": func() string { return strings.Join(st.SettingMarks(), ", ") },
		"auth.origin":        func() string { return e.WebAddress() },
	}
	seen := map[string]bool{}
	for _, def := range store.SettingDefs() {
		// настройки входа живут в реестре, но читает их пакет входа: его
		// читатели проверяет auth.TestSettingDefaultsAreReadByAuth. Движок
		// читает из них только адрес входа — он и проверяется здесь
		if strings.HasPrefix(def.Key, "auth.") && def.Key != "auth.origin" {
			continue
		}
		read, ok := readers[def.Key]
		if !ok {
			t.Errorf("настройка %s есть в реестре, а читателя движка для неё в тесте нет: добавьте строку", def.Key)
			continue
		}
		seen[def.Key] = true
		want := def.ShownDefault()
		if def.Kind == store.SettingFlag {
			want = flagString(def.DefaultFlag())
		}
		if got := read(); got != want {
			t.Errorf("%s: движок на пустой базе читает %q, а умолчание в реестре %q", def.Key, got, want)
		}
	}
	for key := range readers {
		if !seen[key] {
			t.Errorf("в тесте есть читатель %s, а в реестре такой настройки нет", key)
		}
	}
}

func flagString(on bool) string {
	if on {
		return "true"
	}
	return "false"
}

// nightLookMaxSeen — сколько команд осмотра движок берёт из скилла: скилл с
// тридцатью командами, и все они «Разрешить».
func nightLookMaxSeen(t *testing.T, e *Engine, st *store.Store) int {
	t.Helper()
	var cmds []string
	for i := 0; i < 30; i++ {
		cmds = append(cmds, "echo "+strconv.Itoa(i))
	}
	if err := st.SaveSkill(&store.Skill{Name: "linux-admin", Title: "Linux", Group: "Администрирование", Enabled: true,
		Source: store.SkillOwn, Text: "```sema-осмотр\n" + strings.Join(cmds, "\n") + "\n```\n"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveHost(&store.Host{Name: "осмотр", Addr: "10.0.0.9", Port: 22, User: "root", Key: "ssh:осмотр", Kind: store.HostLinux}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddHostRule("осмотр", store.RuleAllow, "echo"); err != nil {
		t.Fatal(err)
	}
	h, _ := st.Host("осмотр")
	_, got := e.nightLookCommands(h)
	return len(got)
}

// nightEveryHoursSeen — через сколько часов движок считает, что смене пора, у
// сервера без окна: смена была двадцать пять часов назад — пора ли.
func nightEveryHoursSeen(t *testing.T, e *Engine) int {
	t.Helper()
	h := &store.Host{Name: "без-окна", Kind: store.HostLinux}
	now := time.Now().Truncate(time.Second) // RFC3339 без долей секунды: граница ровно в час
	for hours := 1; hours <= 200; hours++ {
		if err := e.store.SetSetting(nightLastKey+h.Name, now.Add(-time.Duration(hours)*time.Hour).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		if e.nightDue(h, now) {
			return hours
		}
	}
	return -1
}

// Правка настройки меняет то, что читает движок, и возврат умолчания
// возвращает прежнее чтение: для читателей, чью разницу видно снаружи.
func TestRegisteredSettingChangeIsSeenByEngine(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	h := &store.Host{Name: "проба"}
	for key, want := range map[string]int{
		"mcp.chat_calls": 7, "web.page_chars": 4000, "web.find_gap_ms": 0, "ssh.timeout_s": 15,
		"ssh.confirm_timeout_s": 900, "ssh.max_output": 5000, "auto.max_hour": 9, "auto.rollback_min": 2, "night.max_steps": 4,
	} {
		def, ok := store.LookupSetting(key)
		if !ok {
			t.Fatalf("настройки %s нет в реестре", key)
		}
		if err := st.SaveSetting(def, strconv.Itoa(want)); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		got := map[string]int{
			"mcp.chat_calls": e.MCPChatLimits().Calls, "web.page_chars": e.WebPageChars(), "web.find_gap_ms": e.WebFindGapMS(),
			"ssh.timeout_s": e.SSHTimeout(), "ssh.confirm_timeout_s": e.SSHConfirmTimeout(), "ssh.max_output": e.SSHMaxOutput(),
			"auto.max_hour": e.AutoMaxHour(h), "auto.rollback_min": e.InsuranceMinutes(), "night.max_steps": e.NightMaxSteps(),
		}[key]
		if got != want {
			t.Errorf("%s: записано %d, движок читает %d", key, want, got)
		}
		if err := st.SaveSetting(def, def.Default); err != nil {
			t.Fatal(err)
		}
		back := map[string]int{
			"mcp.chat_calls": e.MCPChatLimits().Calls, "web.page_chars": e.WebPageChars(), "web.find_gap_ms": e.WebFindGapMS(),
			"ssh.timeout_s": e.SSHTimeout(), "ssh.confirm_timeout_s": e.SSHConfirmTimeout(), "ssh.max_output": e.SSHMaxOutput(),
			"auto.max_hour": e.AutoMaxHour(h), "auto.rollback_min": e.InsuranceMinutes(), "night.max_steps": e.NightMaxSteps(),
		}[key]
		if back != def.DefaultInt() {
			t.Errorf("%s: после возврата умолчания движок читает %d, а умолчание %d", key, back, def.DefaultInt())
		}
	}
}
