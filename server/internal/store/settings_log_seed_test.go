package store

// Засев служебных отметок журнала на живую базу. Список отметок — настройка, и
// на базе, где она уже сохранена, новые образцы сами не появятся (грабли
// «Справочник в базе не обновляется сам»): `seedLogMarks` досыпает их по метке
// `settings_log.marks_seeded`, не трогая то, что человек добавил или убрал.

import (
	"strings"
	"testing"
)

func marksOf(st *Store) []string {
	raw, _ := st.settingRow(SettingsLogMarks.Key)
	return ParseSettingMarks(raw)
}

func hasMark(list []string, mark string) bool { return containsMark(list, mark) }

// На базе, где список сохранён первой выкладкой, новые образцы доезжают, а правки
// человека — добавленное и убранное — остаются как были.
func TestSeedLogMarksReachesSavedList(t *testing.T) {
	st := testStore(t)
	// живая база до обновления: засеянное не помнится, список — первая выкладка
	// без «chat.read.» (человек убрал) и с «mine.» (человек добавил)
	st.DeleteSettingKey(settingsLogMarksSeededKey)
	var saved []string
	for _, m := range ParseSettingMarks(settingsLogMarksSeedV1) {
		if m != "chat.read." {
			saved = append(saved, m)
		}
	}
	saved = append(saved, "mine.")
	if err := st.SetSetting(SettingsLogMarks.Key, strings.Join(saved, ", ")); err != nil {
		t.Fatal(err)
	}

	st.seedLogMarks()

	got := marksOf(st)
	for _, mark := range ParseSettingMarks(settingsLogMarksAddedV2) {
		if !hasMark(got, mark) {
			t.Errorf("образец %q не доехал до сохранённого списка", mark)
		}
	}
	if !hasMark(got, "mine.") {
		t.Errorf("добавленное человеком пропало: %v", got)
	}
	if hasMark(got, "chat.read.") {
		t.Errorf("убранное человеком вернулось: %v", got)
	}
	if seeded, _ := st.settingRow(settingsLogMarksSeededKey); !sameList(ParseSettingMarks(seeded), ParseSettingMarks(settingsLogMarksSeed)) {
		t.Errorf("засеянное не запомнилось: %q", seeded)
	}
}

// Образец, который человек убрал уже после засева, повторным запуском не
// возвращается; а образец, которого засев ещё не знал, добавляется.
func TestSeedLogMarksRespectsWhatHumanRemoved(t *testing.T) {
	st := testStore(t)
	current := ParseSettingMarks(settingsLogMarksSeed)
	var without []string // человек убрал «vision.decision.»
	for _, m := range current {
		if m != "vision.decision." {
			without = append(without, m)
		}
	}
	if err := st.SetSetting(SettingsLogMarks.Key, strings.Join(without, ", ")); err != nil {
		t.Fatal(err)
	}
	st.seedLogMarks()
	st.seedLogMarks()
	if got := marksOf(st); hasMark(got, "vision.decision.") {
		t.Errorf("убранное человеком вернулось после повторного запуска: %v", got)
	}

	// следующая версия засева принесла «new.mark.»: засеянное помнит прежний
	// список, а человеку вернуть убранное нельзя, новое — можно. Версию имитируем
	// запомненным списком, из которого вычтен образец «script.»
	var seeded []string
	for _, m := range current {
		if m != "script." {
			seeded = append(seeded, m)
		}
	}
	var saved []string
	for _, m := range without {
		if m != "script." {
			saved = append(saved, m)
		}
	}
	if err := st.SetSetting(SettingsLogMarks.Key, strings.Join(saved, ", ")); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(settingsLogMarksSeededKey, strings.Join(seeded, ", ")); err != nil {
		t.Fatal(err)
	}
	st.seedLogMarks()
	got := marksOf(st)
	if !hasMark(got, "script.") {
		t.Errorf("новый образец не досыпан: %v", got)
	}
	if hasMark(got, "vision.decision.") {
		t.Errorf("убранное человеком вернулось вместе с новым образцом: %v", got)
	}
}

// Список не сохранялся — читается умолчание, и оно уже новое: писать нечего. Пустой
// список, сохранённый человеком, остаётся пустым.
func TestSeedLogMarksLeavesDefaultAndEmptyAlone(t *testing.T) {
	st := testStore(t)
	st.DeleteSettingKey(settingsLogMarksSeededKey)
	st.DeleteSettingKey(SettingsLogMarks.Key)
	st.seedLogMarks()
	if _, saved := st.settingRow(SettingsLogMarks.Key); saved {
		t.Errorf("засев записал список там, где его не сохраняли: умолчание и так новое")
	}
	if !hasMark(st.SettingMarks(), "vision.decision.") {
		t.Errorf("умолчание не знает новых образцов: %v", st.SettingMarks())
	}

	st.DeleteSettingKey(settingsLogMarksSeededKey)
	if err := st.SetSetting(SettingsLogMarks.Key, ""); err != nil {
		t.Fatal(err)
	}
	st.seedLogMarks()
	if got := marksOf(st); len(got) != 0 {
		t.Errorf("пустой список, сохранённый человеком, перестал быть пустым: %v", got)
	}
}

// Служебные образцы работают на деле: решение по картинке и привязка чата,
// подписанные человеком (машина пишет их в долю секунды его действия), не попадают
// в «Ваши правки», но и не пропадают из журнала.
func TestServiceMarksKeepMachineKeysOutOfMine(t *testing.T) {
	st := testStore(t)
	human(t, st, "ui.poll_s", "5")
	human(t, st, "vision.decision.4711", "ok")
	human(t, st, "vision_state", "{}")
	human(t, st, "max.chat.-720", "1")
	human(t, st, "max.history.-720", "100")
	human(t, st, "tg.chat.79991234567.55", "1")
	mine, err := st.SettingsLogView(SettingsLogMine, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(mine); got != "ui.poll_s" {
		t.Errorf("«Ваши правки»: %s", got)
	}
	service, _ := st.SettingsLogView(SettingsLogService, 1000)
	have := map[string]bool{}
	for _, c := range service {
		have[c.Key] = true
	}
	for _, key := range []string{"vision.decision.4711", "vision_state", "max.chat.-720", "max.history.-720", "tg.chat.79991234567.55"} {
		if !have[key] {
			t.Errorf("%s не оказался среди служебных (и не потерян ли: %v)", key, have[key])
		}
	}
}
