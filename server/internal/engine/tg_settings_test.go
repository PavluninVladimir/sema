package engine

// Пределы Telegram читаются через реестр настроек (setting_defs.go), а не
// числом в вызове: экран «Ответы и пределы» показывает умолчание оттуда же.
// Тест держит, что от переезда читателей поведение не сменилось: умолчания те
// же, что были вписаны в коде, мусор и ноль читаются так же, как читали прежние
// `Atoi`, а сохранённое значение доезжает до того, кто его использует.

import (
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestTelegramLimitsReadThroughRegistryAsBefore(t *testing.T) {
	e, st := newTestEngine(t, "", nil)

	// умолчания — те самые числа, что были вписаны рядом с чтением
	defaults := map[string]int{"tg.reply_max_hour": 30, "tg.history": 30, "tg.file_limit_mb": 20, "tg.post_days": 30,
		"tg.chats_max": 2000, "tg.wait_warn_min": 30, "tg.wait_alert_min": 120, "tg.wait_msgs": 3,
		"tg.stream_ms": 1200, "tg.stream_first": 16}
	read := map[string]func() int{
		"tg.reply_max_hour": e.TGReplyMaxHour, "tg.history": e.TGHistoryLimit, "tg.file_limit_mb": e.TGFileLimitMB,
		"tg.post_days":      e.tgPostDays,
		"tg.chats_max":      e.TGChatsMax,
		"tg.wait_warn_min":  func() int { warn, _, _ := e.tgWaitLimits(); return int(warn / time.Minute) },
		"tg.wait_alert_min": func() int { _, alert, _ := e.tgWaitLimits(); return int(alert / time.Minute) },
		"tg.wait_msgs":      func() int { _, _, n := e.tgWaitLimits(); return n },
		"tg.stream_ms":      func() int { return int(e.tgStreamEvery() / time.Millisecond) },
		"tg.stream_first":   e.tgStreamFirst,
	}
	for key, want := range defaults {
		def, ok := store.LookupSetting(key)
		if !ok {
			t.Errorf("%s не в реестре", key)
			continue
		}
		if def.DefaultInt() != want {
			t.Errorf("%s: умолчание реестра %d, а в коде было %d", key, def.DefaultInt(), want)
		}
		if got := read[key](); got != want {
			t.Errorf("%s: на пустой базе читается %d, ждали %d", key, got, want)
		}
	}
	if def, _ := store.LookupSetting("tg.help_word"); def.Default != "сёма" || e.TGHelpWord() != "сёма" {
		t.Errorf("слово-вызов по умолчанию: %q / %q", def.Default, e.TGHelpWord())
	}

	// положительные: пустое, ноль, минус и мусор — «не задан», то есть умолчание;
	// число — оно само, пробелы вокруг не мешают
	for key, want := range defaults {
		if key == "tg.history" || key == "tg.stream_ms" {
			continue
		}
		for raw, expect := range map[string]int{"": want, "0": want, "-3": want, "abc": want, "45": 45, " 45 ": 45} {
			if err := st.SetSetting(key, raw); err != nil {
				t.Fatal(err)
			}
			if got := read[key](); got != expect {
				t.Errorf("%s = %q читается %d, ждали %d", key, raw, got, expect)
			}
		}
	}
	// у истории ноль законен: «не переносить»; минус и мусор — умолчание
	for raw, expect := range map[string]int{"": 30, "0": 0, "-1": 30, "abc": 30, "7": 7} {
		if err := st.SetSetting("tg.history", raw); err != nil {
			t.Fatal(err)
		}
		if got := e.TGHistoryLimit(); got != expect {
			t.Errorf("tg.history = %q читается %d, ждали %d", raw, got, expect)
		}
	}
	// ритм правки ответа: быстрее ста миллисекунд не бывает, что бы ни записали
	for raw, expect := range map[string]time.Duration{"": 1200 * time.Millisecond, "50": 100 * time.Millisecond,
		"800": 800 * time.Millisecond, "0": 1200 * time.Millisecond} {
		if err := st.SetSetting("tg.stream_ms", raw); err != nil {
			t.Fatal(err)
		}
		if got := e.tgStreamEvery(); got != expect {
			t.Errorf("tg.stream_ms = %q даёт паузу %s, ждали %s", raw, got, expect)
		}
	}
	// слово-вызов читается обрезанным, пустое — умолчанием
	for raw, expect := range map[string]string{"  бот ": "бот", "": "сёма", "   ": "сёма", "Сёма": "Сёма"} {
		if err := st.SetSetting("tg.help_word", raw); err != nil {
			t.Fatal(err)
		}
		if got := e.TGHelpWord(); got != expect {
			t.Errorf("tg.help_word = %q читается %q, ждали %q", raw, got, expect)
		}
	}
}
