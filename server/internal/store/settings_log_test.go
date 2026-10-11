package store

// Проверки журнала изменений настроек. Главная из них — про секреты:
// журнал человек присылает в переписку, когда что-то сломалось, и
// значения ключей в нём быть не должно ни при каком пути.

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/secret"
)

func TestSettingChangeLogsItself(t *testing.T) {
	st := testStore(t)
	before := len(changesByKey(t, st, "queue.pause_ms"))
	if err := st.SetSetting("queue.pause_ms", "3000"); err != nil {
		t.Fatalf("настройка: %v", err)
	}
	if err := st.SetSetting("queue.pause_ms", "500"); err != nil {
		t.Fatalf("настройка: %v", err)
	}
	list := changesByKey(t, st, "queue.pause_ms")
	if len(list) != before+2 {
		t.Fatalf("правок в журнале %d, ждали %d: %+v", len(list), before+2, list)
	}
	if list[0].Key != "queue.pause_ms" || list[0].Before != "3000" || list[0].After != "500" {
		t.Fatalf("верхняя правка не та: %+v", list[0])
	}
	if list[0].Who != "" {
		t.Fatalf("правку никто не заявлял, а подписана как %q", list[0].Who)
	}
}

func TestSameValueIsNotAChange(t *testing.T) {
	st := testStore(t)
	_ = st.SetSetting("notify.keep_days", "30")
	_ = st.SetSetting("notify.keep_days", "30")
	_ = st.SetSetting("notify.keep_days", "30")
	list := changesByKey(t, st, "notify.keep_days")
	if len(list) != 1 {
		t.Fatalf("правок %d, ждали одну: перезапись тем же значением ничего не меняет", len(list))
	}
}

func TestSettingRemovalVisibleInLog(t *testing.T) {
	st := testStore(t)
	_ = st.SetSetting("chat.read.7", "12")
	st.DeleteSettingKey("chat.read.7")
	list := changesByKey(t, st, "chat.read.7")
	if len(list) != 2 || !list[0].Removed || list[0].Before != "12" {
		t.Fatalf("удаление параметра не записано: %+v", list)
	}
}

func TestSignatureOnlyOnUnclaimed(t *testing.T) {
	st := testStore(t)
	_ = st.SetSetting("ui.poll_s", "4")
	mark := st.LastChange()
	_ = st.SetSetting("ui.poll_s", "8")
	st.SignChanges(mark, "вы", "settings:notify · save_notify")
	// весь журнал, а не первые десять строк: миграции при заведении базы пишут
	// свои метки, и их число растёт с каждой новой миграцией
	list, _ := st.SettingsLog("", 1000)
	if list[0].Who != "вы" || list[0].From != "settings:notify · save_notify" {
		t.Fatalf("правка действия не подписана: %+v", list[0])
	}
	if list[1].Who != "" {
		t.Fatalf("подпись задним числом дотянулась до правки до метки: %+v", list[1])
	}
	byHumans, _ := st.SettingsLog("люди", 100)
	if len(byHumans) != 1 {
		t.Fatalf("в «ваших правках» %d строк, ждали одну", len(byHumans))
	}
	bySelf, _ := st.SettingsLog("сам", 100)
	if len(bySelf) == 0 || len(bySelf)+1 != len(list) {
		t.Fatalf("«Сёма сам» и «ваши правки» вместе не дают весь журнал: %d + 1 против %d", len(bySelf), len(list))
	}
}

// changesByKey — журнал одного параметра. Миграция при заведении базы
// сама пишет несколько настроек (и это ровно то, что дверь обязана
// ловить), поэтому счёт по всему журналу в тесте бессмыслен.
func changesByKey(t *testing.T, st *Store, key string) []SettingChange {
	t.Helper()
	all, err := st.SettingsLog("", 1000)
	if err != nil {
		t.Fatalf("журнал: %v", err)
	}
	var out []SettingChange
	for _, chg := range all {
		if chg.Key == key {
			out = append(out, chg)
		}
	}
	return out
}

// Главная проверка: значение секрета в журнал не попадает ни одним из
// путей, которыми настройка вообще меняется.
func TestSecretValueNeverReachesLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "секреты.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("база: %v", err)
	}
	defer st.Close()
	box, err := secret.New("проба-ключа-для-теста")
	if err != nil {
		t.Fatalf("ключ: %v", err)
	}
	st.UseSecretBox(box)

	const providerKey = "sk-proj-7SbQ2xVuLmNp4RtYwZaKdE9fGhJcX1"
	const password = "ОченьСекретныйПароль2026"
	const accountToken = "y0_AgAAAABmGtQqAAvXYZqQwErTyUiOpAsDfGhJkL"

	// 1. закрытая настройка — значение шифруется на входе
	if err := st.SetSecretSetting("tg.api_hash", providerKey); err != nil {
		t.Fatalf("закрытая настройка: %v", err)
	}
	// 2. обычная настройка, у которой имя говорит о секрете: без ключа
	// шифрования такое значение легло бы открытым текстом
	if err := st.SetSetting("google.refresh_token", accountToken); err != nil {
		t.Fatalf("настройка по имени: %v", err)
	}
	// 3. секрет внутри обычной настройки: ключ провайдера в списке моделей
	if err := st.SetSetting("models.json",
		`[{"name":"DeepseekPro","api_key":"`+providerKey+`","base":"https://api.deepseek.com"}]`); err != nil {
		t.Fatalf("список моделей: %v", err)
	}
	// 4. пароль ящика внутри настроек почты
	if err := st.SetSetting("mail.conf.me@example.com",
		`{"host":"imap.yandex.ru","user":"vp","password":"`+password+`"}`); err != nil {
		t.Fatalf("настройки ящика: %v", err)
	}
	// 5. заголовок с ключом у сервера MCP
	if err := st.SetSetting("mcp.headers.jira",
		`{"Authorization":"Bearer `+providerKey+`"}`); err != nil {
		t.Fatalf("заголовки: %v", err)
	}
	// 6. правка того же секрета вторым заходом: в журнале появится не
	// только «стало», но и «было» — прежнее значение прячется наравне
	if err := st.SetSecretSetting("tg.api_hash", password); err != nil {
		t.Fatalf("замена секрета: %v", err)
	}

	// 7. закрытая настройка, когда ключа шифрования нет вовсе: `seal`
	// возвращает значение как есть, имя параметра ни о чём не говорит, и
	// спасает только пометка «это секрет» на входе в дверь
	noKey, err := Open(filepath.Join(t.TempDir(), "безключа.db"))
	if err != nil {
		t.Fatalf("база без ключа: %v", err)
	}
	defer noKey.Close()
	if err := noKey.SetSecretSetting("yadisk.gate", accountToken); err != nil {
		t.Fatalf("закрытая настройка без ключа: %v", err)
	}
	if strings.Contains(dumpLog(t, noKey), accountToken) {
		t.Fatalf("секрет без ключа шифрования лёг в журнал открытым текстом")
	}

	list, err := st.SettingsLog("", 100)
	if err != nil {
		t.Fatalf("журнал: %v", err)
	}
	if len(list) < 6 {
		t.Fatalf("правок в журнале %d, ждали хотя бы шесть", len(list))
	}
	for _, chg := range list {
		for _, field := range []string{chg.Before, chg.After} {
			for _, secret := range []string{providerKey, password, accountToken} {
				if strings.Contains(field, secret) {
					t.Fatalf("секрет утёк в журнал настроек: ключ %q, значение %q", chg.Key, field)
				}
			}
		}
	}

	// и то же самое по всей таблице целиком: значение могло уехать не в
	// то поле, которое читает SettingsLog, — например в «откуда»
	for _, secret := range []string{providerKey, password, accountToken} {
		if strings.Contains(dumpLog(t, st), secret) {
			t.Fatalf("секрет нашёлся в таблице settings_log")
		}
	}
}

// dumpLog — вся таблица журнала одной строкой, все поля.
func dumpLog(t *testing.T, st *Store) string {
	t.Helper()
	rows, err := st.db.Query(`SELECT at, key, old_val, new_val, who, source FROM settings_log`)
	if err != nil {
		t.Fatalf("дамп журнала: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var at, key, old, nw, who, src string
		if err := rows.Scan(&at, &key, &old, &nw, &who, &src); err != nil {
			t.Fatalf("дамп журнала: %v", err)
		}
		b.WriteString(strings.Join([]string{at, key, old, nw, who, src}, "|") + "\n")
	}
	return b.String()
}

func TestThresholdWithSecretishNameStaysVisible(t *testing.T) {
	st := testStore(t)
	// «session» в имени — но это число часов, а не токен: прятать его
	// значило бы прятать половину настроек
	_ = st.SetSetting("memory.session_idle_h", "8")
	list := changesByKey(t, st, "memory.session_idle_h")
	if list[0].Hidden || list[0].After != "8" {
		t.Fatalf("порог спрятали как секрет: %+v", list[0])
	}
}

func TestChangeLogDoesNotGrowForever(t *testing.T) {
	st := testStore(t)
	for i := 0; i < keepSettingsLogPerKey+50; i++ {
		_ = st.SetSetting("folders.cursor.vp.INBOX", strconv.Itoa(i))
	}
	list := changesByKey(t, st, "folders.cursor.vp.INBOX")
	if len(list) > keepSettingsLogPerKey {
		t.Fatalf("правок одного параметра %d, предел %d", len(list), keepSettingsLogPerKey)
	}
	if list[0].After != strconv.Itoa(keepSettingsLogPerKey+49) {
		t.Fatalf("последняя правка потерялась: %+v", list[0])
	}
}
