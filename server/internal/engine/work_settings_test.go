package engine

// «Работа Сёмы» (2 октября 2026): провод и опрос экрана, чаты команд, пинатель
// и хвост ветки переехали с «Уведомлений» на свой раздел, а их читатели — на
// реестр настроек (`setting_defs.go`). Тест держит, что поведение при этом не
// сменилось: умолчания — те самые числа, что были вписаны рядом с чтением (их
// выписали из прежнего кода, а не из реестра), ноль и мусор читаются как
// читали прежние `intSetting` и `Atoi`, а сохранённое доезжает до того, кто
// его использует.

import (
	"strconv"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestWorkSettingsReadThroughRegistryAsBefore(t *testing.T) {
	e, st := newTestEngine(t, "", nil)

	ms := func(d time.Duration) int { return int(d / time.Millisecond) }
	sec := func(d time.Duration) int { return int(d / time.Second) }
	live := func() LiveSettings { return e.LiveSettings() }
	read := map[string]func() int{
		"ui.live_ms":            func() int { return ms(live().Window) },
		"ui.poll_s":             func() int { return live().PollS },
		"ui.poll_live_s":        func() int { return live().PollLiveS },
		"ui.ws_write_s":         func() int { return sec(live().WriteWait) },
		"ui.ws_per_session":     func() int { return live().PerSession },
		"ui.snapshot_days":      func() int { return live().SnapshotDays },
		"chats.command_ttl_h":   e.CommandChatTTL,
		"chats.nudge_after_min": e.NudgeAfterMin,
		"chats.nudge_max":       e.NudgeMax,
		"chats.thread_tail":     e.ThreadTail,
	}
	// прежние умолчания, выписанные из прежнего кода: `defaultLiveMS` и родня в
	// live.go, `24` в CommandChatTTL, `5` и `3` в nudge.go, `12` в ThreadTail
	was := map[string]int{
		"ui.live_ms": 300, "ui.poll_s": 4, "ui.poll_live_s": 60, "ui.ws_write_s": 10, "ui.ws_per_session": 8,
		"ui.snapshot_days": 7, "chats.command_ttl_h": 24, "chats.nudge_after_min": 5, "chats.nudge_max": 3,
		"chats.thread_tail": 12,
	}
	if len(was) != len(read) {
		t.Fatalf("настроек в таблице прежних чисел %d, читателей %d", len(was), len(read))
	}
	for key, want := range was {
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

	// положительные: пустое, ноль, минус и мусор — «не задан», то есть
	// умолчание; число — оно само. Так читал `intSetting` (и `Atoi` с «≤ 0 —
	// умолчание» у срока чатов команд и у паузы пинателя)
	for key, want := range was {
		if key == "chats.nudge_max" {
			continue
		}
		for raw, expect := range map[string]int{"": want, "0": want, "-3": want, "abc": want, "45": 45} {
			if err := st.SetSetting(key, raw); err != nil {
				t.Fatal(err)
			}
			if got := read[key](); got != expect {
				t.Errorf("%s = %q читается %d, ждали %d", key, raw, got, expect)
			}
		}
		_ = st.SetSetting(key, "")
	}

	// у пинков ноль законен: «не пинать». Минус и мусор раньше читались нулём
	// (`Atoi` с проглоченной ошибкой), теперь — умолчанием, как у любого «счёта»
	// реестра; записать такое с экрана нельзя (границы 0–20), поэтому на живых
	// данных разницы нет
	for raw, expect := range map[string]int{"0": 0, "1": 1, "20": 20} {
		if err := st.SetSetting("chats.nudge_max", raw); err != nil {
			t.Fatal(err)
		}
		if got := e.NudgeMax(); got != expect {
			t.Errorf("chats.nudge_max = %q читается %d, ждали %d", raw, got, expect)
		}
	}
	for _, raw := range []string{"-1", "abc", ""} {
		if err := st.SetSetting("chats.nudge_max", raw); err != nil {
			t.Fatal(err)
		}
		if got := e.NudgeMax(); got != 3 {
			t.Errorf("chats.nudge_max = %q читается %d, ждали умолчание 3", raw, got)
		}
	}
}

// Сохранённое тем же путём, что и с экрана (`SaveSetting`: проверка по
// описанию), доезжает до читателей движка — тех, кого зовут срок чата команды,
// пинатель, ветка и подписка на кадры. Возврат умолчания возвращает прежнее
// чтение.
func TestWorkSettingsChangeReachesUsers(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	save := func(key, value string) {
		t.Helper()
		def, ok := store.LookupSetting(key)
		if !ok {
			t.Fatalf("%s не в реестре", key)
		}
		if err := st.SaveSetting(def, value); err != nil {
			t.Fatalf("%s = %s: %v", key, value, err)
		}
	}

	save("chats.command_ttl_h", "6")
	if e.CommandChatTTL() != 6 {
		t.Errorf("срок чата команды %d, а записано 6", e.CommandChatTTL())
	}
	save("chats.nudge_after_min", "9")
	if got := e.nudgeAfter(); got != 9*time.Minute {
		t.Errorf("пинать через %s, а записано 9 минут", got)
	}
	save("chats.nudge_max", "0")
	if e.NudgeMax() != 0 {
		t.Errorf("пинков %d, а записан 0 («не пинать»)", e.NudgeMax())
	}
	save("chats.thread_tail", "20")
	if e.ThreadTail() != 20 {
		t.Errorf("хвост ветки %d, а записано 20", e.ThreadTail())
	}
	save("ui.live_ms", "1500")
	save("ui.poll_s", "7")
	save("ui.poll_live_s", "90")
	save("ui.ws_write_s", "20")
	save("ui.ws_per_session", "3")
	save("ui.snapshot_days", "30")
	live := e.LiveSettings()
	if live.Window != 1500*time.Millisecond || live.PollS != 7 || live.PollLiveS != 90 ||
		live.WriteWait != 20*time.Second || live.PerSession != 3 || live.SnapshotDays != 30 {
		t.Errorf("движок читает не то, что записано: %+v", live)
	}

	// возврат умолчания тем же путём
	for _, key := range []string{"chats.command_ttl_h", "chats.nudge_after_min", "chats.nudge_max", "chats.thread_tail",
		"ui.live_ms", "ui.poll_s", "ui.poll_live_s", "ui.ws_write_s", "ui.ws_per_session", "ui.snapshot_days"} {
		def, _ := store.LookupSetting(key)
		save(key, def.Default)
	}
	live = e.LiveSettings()
	if live.Window != 300*time.Millisecond || live.PollS != 4 || live.PollLiveS != 60 ||
		live.WriteWait != 10*time.Second || live.PerSession != 8 || live.SnapshotDays != 7 ||
		e.CommandChatTTL() != 24 || e.nudgeAfter() != 5*time.Minute || e.NudgeMax() != 3 || e.ThreadTail() != 12 {
		t.Errorf("после возврата умолчаний: %+v, срок %d, пинок %s/%d, хвост %d",
			live, e.CommandChatTTL(), e.nudgeAfter(), e.NudgeMax(), e.ThreadTail())
	}
}

// Границы реестра — те, что стояли у шагов на «Уведомлениях»: экран больше не
// даёт вписать то, что прежний шаг не показывал.
func TestWorkSettingsBoundsAreThoseOfOldSteppers(t *testing.T) {
	for key, bounds := range map[string][2]int{
		"ui.live_ms": {100, 2000}, "ui.poll_s": {2, 60}, "ui.poll_live_s": {10, 600}, "ui.ws_write_s": {3, 60},
		"ui.ws_per_session": {1, 32}, "ui.snapshot_days": {1, 90}, "chats.command_ttl_h": {1, 720},
		"chats.nudge_after_min": {1, 120}, "chats.nudge_max": {0, 20}, "chats.thread_tail": {1, 60},
	} {
		def, ok := store.LookupSetting(key)
		if !ok {
			t.Errorf("%s не в реестре", key)
			continue
		}
		if def.Min != bounds[0] || def.Max != bounds[1] {
			t.Errorf("%s: границы реестра %d–%d, а у шага были %d–%d", key, def.Min, def.Max, bounds[0], bounds[1])
		}
		if _, err := def.Check(strconv.Itoa(bounds[0] - 1)); err == nil {
			t.Errorf("%s: %d ниже нижней границы принято", key, bounds[0]-1)
		}
		if _, err := def.Check(strconv.Itoa(bounds[1] + 1)); err == nil {
			t.Errorf("%s: %d выше верхней границы принято", key, bounds[1]+1)
		}
		if _, err := def.Check(strconv.Itoa(bounds[1])); err != nil {
			t.Errorf("%s: верхняя граница %d отвергнута: %v", key, bounds[1], err)
		}
	}
}
