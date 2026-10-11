package store

// Журнал настроек глазами человека: «ваши правки» отдельно от служебных
// отметок, ничего не удаляется, список отметок — настройка.

import (
	"strings"
	"testing"
	"time"
)

// human — правка, подписанная человеком: так её метит `SignChanges`.
func human(t *testing.T, st *Store, key, value string) {
	t.Helper()
	mark := st.LastChange()
	if err := st.SetSetting(key, value); err != nil {
		t.Fatal(err)
	}
	st.SignChanges(mark, "вы", "settings:x · save_x")
}

func keysOf(list []SettingChange) string {
	var keys []string
	for _, c := range list {
		keys = append(keys, c.Key)
	}
	return strings.Join(keys, ",")
}

// По умолчанию журнал — «Ваши правки»: подписано человеком и не служебная
// отметка. Служебные — всё остальное, «Все» — как лежит; сумма двух отборов равна
// журналу целиком, и ни одна запись не потеряна.
func TestSettingsLogViewSplitsMineAndService(t *testing.T) {
	st := testStore(t)
	// вычитаем то, что заводит сама база: миграции пишут свои метки
	_, serviceBefore, totalBefore := st.CountSettingsLogView()
	human(t, st, "ui.poll_s", "5")
	human(t, st, "tg.post_days", "40")
	// курсор, записанный в долю секунды человеческого действия, подписан его именем,
	// но правкой не считается: по ключу он служебный
	human(t, st, "chat.read.71", "10")
	_ = st.SetSetting("folders.cursor.vp.INBOX", "100")
	_ = st.SetSetting("notify.mac_last_err", "01.10 · нет связи")
	_ = st.SetSetting("backup.min_bps", "4096") // не отметка, но и не подписана

	mine, err := st.SettingsLogView(SettingsLogMine, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(mine); got != "tg.post_days,ui.poll_s" {
		t.Errorf("«Ваши правки»: %s", got)
	}
	service, _ := st.SettingsLogView(SettingsLogService, 1000)
	for _, c := range service {
		if c.Key == "ui.poll_s" || c.Key == "tg.post_days" {
			t.Errorf("правка человека попала в служебные: %s", c.Key)
		}
	}
	all, _ := st.SettingsLogView(SettingsLogAll, 1000)
	nMine, nService, nTotal := st.CountSettingsLogView()
	if nMine != len(mine) || nService != len(service) || nTotal != len(all) || nMine+nService != nTotal {
		t.Errorf("счёт отборов расходится с самими отборами: %d+%d≠%d (%d, %d, %d)", nMine, nService, nTotal, len(mine), len(service), len(all))
	}
	if nTotal != totalBefore+6 || nService != serviceBefore+4 {
		t.Errorf("записей стало %d (служебных %d), ждали +6 и +4 к %d и %d: ничего не потеряно", nTotal, nService, totalBefore, serviceBefore)
	}
	// прежние слова отбора (закладки) читаются как нынешние
	old, _ := st.SettingsLogView("люди", 100)
	if keysOf(old) != keysOf(mine) {
		t.Errorf("«люди» читается не как «ваши правки»: %s", keysOf(old))
	}
}

// Список отметок — настройка, а не код: поменяли строку — другая запись стала
// служебной. Приставка, окончание и «содержит» — три вида образца.
func TestSettingsLogMarksComeFromSetting(t *testing.T) {
	st := testStore(t)
	human(t, st, "ui.poll_s", "5")
	human(t, st, "alpha.cursor.1", "1")
	human(t, st, "beta.last_err", "x")
	human(t, st, "gamma.zz.delta", "1")
	if got := keysOf(must(st.SettingsLogView(SettingsLogMine, 100))); !strings.Contains(got, "alpha.cursor.1") {
		t.Fatalf("до настройки отметок ничего не спрятано: %s", got)
	}
	if err := st.SetSetting(SettingsLogMarks.Key, "alpha.cursor., *.last_err, *.zz.*"); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(must(st.SettingsLogView(SettingsLogMine, 100))); strings.Contains(got, "alpha") || strings.Contains(got, "beta") ||
		strings.Contains(got, "gamma") || !strings.Contains(got, "ui.poll_s") {
		t.Errorf("отметки из настройки не спрятали своё: %s", got)
	}
	// пустой список — служебных отметок нет, прячется только то, что без подписи
	if err := st.SetSetting(SettingsLogMarks.Key, ""); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(must(st.SettingsLogView(SettingsLogMine, 100))); !strings.Contains(got, "alpha.cursor.1") {
		t.Errorf("пустой список отметок всё ещё прячет: %s", got)
	}
	for key, marks := range map[string][]string{
		"chat.read.71": {"chat.read."}, "a.b.last_err": {"*.last_err"}, "x.y.z": {"*.y.*"}, "chat.read": {"chat.read."},
	} {
		want := key != "chat.read"
		if IsServiceMark(key, marks) != want {
			t.Errorf("IsServiceMark(%q, %v) = %v", key, marks, !want)
		}
	}
	// знаки LIKE в образце — обычные знаки
	_ = st.SetSetting(SettingsLogMarks.Key, "a_b.")
	human(t, st, "axb.q", "1")
	human(t, st, "a_b.q", "1")
	if got := keysOf(must(st.SettingsLogView(SettingsLogMine, 100))); !strings.Contains(got, "axb.q") || strings.Contains(got, "a_b.q") {
		t.Errorf("«_» в образце сработал как знак LIKE: %s", got)
	}
}

func must(list []SettingChange, err error) []SettingChange {
	if err != nil {
		panic(err)
	}
	return list
}

// «Под рукой» считает по журналу за месяц: чаще правленное выше, служебные и
// секреты не в счёте, правки старше срока не считаются.
func TestTopEditedKeys(t *testing.T) {
	st := testStore(t)
	for i := 0; i < 5; i++ {
		human(t, st, "ui.poll_s", strings.Repeat("1", i+1))
	}
	for i := 0; i < 3; i++ {
		human(t, st, "tg.post_days", strings.Repeat("2", i+1))
	}
	human(t, st, "search.save_pages", "2")
	for i := 0; i < 9; i++ {
		human(t, st, "chat.read.71", strings.Repeat("3", i+1)) // служебная, даже подписанная
	}
	// правка секрета: значение скрыто, под рукой её выносить нечем
	mark := st.LastChange()
	if err := st.SetSecretSetting("google.refresh_token", "значение"); err != nil {
		t.Fatal(err)
	}
	st.SignChanges(mark, "вы", "settings:x · save_x")
	// старая правка: за месяц её нет
	if _, err := st.db.Exec(`INSERT INTO settings_log (at, key, old_val, new_val, who, source)
		VALUES (?, 'mail.old_one', '1', '2', 'вы', 'x · y')`, time.Now().AddDate(0, 0, -45).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	since := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
	top, err := st.TopEditedKeys(since, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range top {
		got = append(got, k.Key)
	}
	if strings.Join(got, ",") != "ui.poll_s,tg.post_days,search.save_pages" {
		t.Errorf("самые частые за месяц: %v", got)
	}
	if top[0].Count != 5 || top[0].Source != "settings:x · save_x" {
		t.Errorf("счёт и источник последней правки: %+v", top[0])
	}
}

func TestChangedKeysSince(t *testing.T) {
	st := testStore(t)
	mark := st.LastChange()
	if len(st.ChangedKeysSince(mark)) != 0 {
		t.Fatal("после метки ничего не менялось, а ключи есть")
	}
	_ = st.SetSetting("ui.poll_s", "7")
	_ = st.SetSetting("ui.poll_s", "8")
	_ = st.SetSetting("tg.post_days", "9")
	if got := strings.Join(st.ChangedKeysSince(mark), ","); !(got == "ui.poll_s,tg.post_days" || got == "tg.post_days,ui.poll_s") {
		t.Errorf("изменённые ключи без повторов: %s", got)
	}
}
