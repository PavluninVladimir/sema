package store

import "testing"

// Вид настройки решает, как читается ноль и мусор: у «положительной» они —
// «не задана», у «счёта» ноль законен. Так читали прежние читатели
// (`settingInt` и `settingCount`), и поведение не должно сместиться.
func TestSettingKindsReadZeroAndGarbageAsBefore(t *testing.T) {
	st := testStore(t)
	positive := DefPositive("проба.положительная", 7, 1, 100)
	count := DefCount("проба.счёт", 7, 0, 100)
	for _, c := range []struct {
		stored         string
		wantPos, wantN int
	}{
		{"", 7, 7}, {"0", 7, 0}, {"-3", 7, 7}, {"много", 7, 7}, {"12", 12, 12}, {"12 штук", 12, 12},
	} {
		if err := st.SetSetting("проба.положительная", c.stored); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSetting("проба.счёт", c.stored); err != nil {
			t.Fatal(err)
		}
		if got := st.SettingInt(positive); got != c.wantPos {
			t.Errorf("положительная, в базе %q: читается %d, а ждали %d", c.stored, got, c.wantPos)
		}
		if got := st.SettingInt(count); got != c.wantN {
			t.Errorf("счёт, в базе %q: читается %d, а ждали %d", c.stored, got, c.wantN)
		}
	}
}

func TestSettingFlagReadsOldSpellings(t *testing.T) {
	st := testStore(t)
	flag := DefFlag("проба.флаг", true)
	off := DefFlag("проба.флаг-выкл", false)
	for stored, want := range map[string]bool{"": true, "1": true, "0": false, "false": false, "off": false, "нет": false, "true": true, "что-то": true} {
		if err := st.SetSetting("проба.флаг", stored); err != nil {
			t.Fatal(err)
		}
		if got := st.SettingFlag(flag); got != want {
			t.Errorf("флаг, в базе %q: %v, а ждали %v", stored, got, want)
		}
	}
	if st.SettingFlag(off) || st.SettingShown(off) != "false" || off.ShownDefault() != "false" {
		t.Error("умолчание «выключено» читается включённым")
	}
}

// Проверка значения с экрана: границы и вид; записанное читается обратно.
func TestSettingCheckAndSave(t *testing.T) {
	st := testStore(t)
	d := DefCount("проба.предел", 3, 0, 10)
	for raw, ok := range map[string]bool{"0": true, "10": true, " 5 ": true, "11": false, "-1": false, "": false, "пять": false} {
		if _, err := d.Check(raw); (err == nil) != ok {
			t.Errorf("Check(%q): ошибка %v, ждали ok=%v", raw, err, ok)
		}
	}
	if err := st.SaveSetting(d, " 8 "); err != nil || st.SettingInt(d) != 8 || st.SettingShown(d) != "8" {
		t.Errorf("запись не читается: %v %d", err, st.SettingInt(d))
	}
	if err := st.SaveSetting(d, "99"); err == nil || st.SettingInt(d) != 8 {
		t.Errorf("плохое значение что-то записало: %v %d", err, st.SettingInt(d))
	}
	noMax := DefPositive("проба.без-верха", 1, 500, 0)
	if _, err := noMax.Check("1000000"); err != nil {
		t.Errorf("без верхней границы большое число отвергнуто: %v", err)
	}
	if _, err := noMax.Check("499"); err == nil {
		t.Error("ниже нижней границы принято")
	}
	flag := DefFlag("проба.тумблер", true)
	if v, err := flag.Check("false"); err != nil || v != "0" {
		t.Errorf("Check(false) = %q %v", v, err)
	}
	if _, err := flag.Check("может"); err == nil {
		t.Error("непонятный тумблер принят")
	}
}

// Одно описание на ключ: второе — это и есть второй список чисел.
func TestSettingDefinedTwiceFails(t *testing.T) {
	DefText("проба.дважды", "а")
	defer func() {
		if recover() == nil {
			t.Error("повторное описание ключа не уронило запуск")
		}
	}()
	DefText("проба.дважды", "б")
}
