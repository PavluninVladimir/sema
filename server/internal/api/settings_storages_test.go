package api

// «Хранилища» (2 октября 2026): список тонкими строками, экран хранилища с итогом
// проверки и параметрами полями, «Добавить хранилище» отдельным экраном, папки с
// «Настройками» в раскрытии. Поведение файлов и зеркала не менялось.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/storage"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Строка списка тонкая: не больше двух капсул, одна кнопка, вся строка ведёт на
// экран хранилища. Итог проверки и её ошибка в строке не стоят — там одна капсула.
func TestStoragesListRowsAreThin(t *testing.T) {
	s, _ := stnFix(t)
	doc, err := s.buildScreen(storagesScreen, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"local", "yandex", "диск", "старый-ftp"} {
		row := findRow(doc, "storage-"+name)
		if row == nil {
			t.Fatalf("строки хранилища %q нет", name)
		}
		if len(row.Badges) > 2 {
			t.Errorf("%s: капсул %d: %+v", name, len(row.Badges), row.Badges)
		}
		if n := stnVisibleButtons(row); n > 1 {
			t.Errorf("%s: кнопок в строке %d: %+v", name, n, row.Actions)
		}
		var goTo string
		for _, a := range row.Actions {
			if a.Action == "$goto" {
				goTo = a.Target
			}
		}
		if goTo != storageAddr(name) {
			t.Errorf("%s: строка ведёт на %q, а экран хранилища — %q", name, goTo, storageAddr(name))
		}
		if strings.Contains(row.Caption, "no such host") {
			t.Errorf("%s: ошибка проверки в строке списка, а она живёт на экране хранилища: %q", name, row.Caption)
		}
	}
	// у зеркала, которое не отвечает, — капсула «не отвечает» и «зеркало»
	row := findRow(doc, "storage-yandex")
	texts := []string{}
	for _, b := range row.Badges {
		texts = append(texts, b.Text)
	}
	if strings.Join(texts, "|") != "не отвечает|зеркало" || row.Level != LevelAlert {
		t.Errorf("капсулы зеркала с ошибкой проверки: %v, уровень %q", texts, row.Level)
	}
	// второе зеркало названо честно: Сёма в него не пишет
	spare := findRow(doc, "storage-диск")
	if spare == nil || !strings.Contains(spare.Caption, "в него не пишет") {
		t.Errorf("второе зеркало не сказало, что Сёма в него не пишет: %+v", spare)
	}
	// местное не убирается: в строке «···» нет, а «основное» — единственная капсула
	local := findRow(doc, "storage-local")
	if len(local.Menu) != 0 || len(local.Badges) != 1 || local.Badges[0].Text != "основное" {
		t.Errorf("местное хранилище: меню %+v, капсулы %+v", local.Menu, local.Badges)
	}
	// у остальных «Убрать…» в меню — с вопросом, не в ряду кнопок
	if len(row.Menu) != 1 || row.Menu[0].Action != "delete_storage" || row.Menu[0].Confirm == "" || row.Menu[0].ConfirmOK == "" {
		t.Errorf("«Убрать…» у зеркала: %+v", row.Menu)
	}
	// в шапке раздела одна кнопка «Добавить хранилище» на обеих вкладках
	for _, screen := range []string{storagesScreen, storagesScreen + "~" + storTabSources} {
		d, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.ToolbarActions) != 1 || d.ToolbarActions[0].Label != "Добавить хранилище" || d.ToolbarActions[0].Target != entityAddr(storagesScreen, storNew, "") {
			t.Errorf("%s: кнопки шапки %+v", screen, d.ToolbarActions)
		}
	}
	// формы добавления под списком больше нет: форма стоит на своём экране
	for _, id := range []string{"add-storage", "add-yadisk"} {
		if findRow(doc, id) != nil {
			t.Errorf("форма %s осталась под списком", id)
		}
	}
}

// Открытие экранов «Хранилищ» не ходит в сеть и не читает ключи: счёт файлов у внешнего
// хранилища — это список объектов у S3 (до минуты) и чтение секретов с записью в их журнал при
// каждом открытии. Сколько файлов уехало, знает база; ходит в сеть только «Проверить».
func TestStorageScreensNeverTouchSecretsOrNetwork(t *testing.T) {
	s, st := stnFix(t)
	before := st.CountSecretLogFiltered("s3-access", store.SecretLogAll) + st.CountSecretLogFiltered("s3-secret", store.SecretLogAll) +
		st.CountSecretLogFiltered("disk-token", store.SecretLogAll)
	for _, screen := range []string{storagesScreen, storagesScreen + "~" + storTabSources, storageAddr("yandex"), storageAddr("диск"),
		storageAddr("local"), entityAddr(storagesScreen, storNew, "")} {
		mustDoc(t, s, screen)
	}
	after := st.CountSecretLogFiltered("s3-access", store.SecretLogAll) + st.CountSecretLogFiltered("s3-secret", store.SecretLogAll) +
		st.CountSecretLogFiltered("disk-token", store.SecretLogAll)
	if after != before {
		t.Errorf("открытие экранов хранилищ прочитало ключи: в журнале секретов было %d записей, стало %d", before, after)
	}
}

// Экран хранилища: итог проверки и её ошибка живут здесь, там же — сколько файлов
// и сколько ждёт зеркала, ключи ссылкой на секрет, «Опасное» последним.
func TestStorageScreenCheckCountsAndKeys(t *testing.T) {
	s, _ := stnFix(t)
	doc, err := s.buildScreen(storageAddr("yandex"), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := findRow(doc, "storage-check")
	if check == nil || check.Level != LevelAlert || !strings.Contains(check.Caption, "no such host") {
		t.Fatalf("ошибка проверки не видна на экране хранилища: %+v", check)
	}
	if strings.Contains(check.Caption, "ошибка:") {
		t.Errorf("слово «ошибка:» осталось в тексте ошибки: %q", check.Caption)
	}
	files := findRow(doc, "storage-files")
	if files == nil {
		t.Fatal("нет строки про файлы")
	}
	facts := map[string]string{}
	for _, f := range files.Facts {
		facts[f.Label] = f.Value
	}
	if facts["Лежит в зеркале"] != "1 из 3" || facts["Ждёт зеркала"] != "2" {
		t.Errorf("счёт файлов зеркала: %v", facts)
	}
	// «Проверить» — главная кнопка шапки, ключи названы именем секрета со ссылкой
	if len(doc.Components) < 2 || doc.Components[0].Type != "crumb" || doc.Components[1].ID != "entity" {
		t.Fatalf("шапка сущности не первой: %+v", doc.Components[0])
	}
	head := doc.Components[1].Children[0]
	if len(head.Actions) != 1 || head.Actions[0].Action != "check_storage" || head.Actions[0].Target != "yandex" {
		t.Errorf("главная кнопка шапки: %+v", head.Actions)
	}
	for n, secret := range []string{"s3-access", "s3-secret"} {
		row := findRow(doc, fmt.Sprintf("storage-key-%d", n))
		if row == nil || len(row.Actions) != 1 || row.Actions[0].Target != engine.SecretScreen(secret, "") || !strings.Contains(row.Caption, secret) {
			t.Errorf("ключ %d: ссылка на секрет %q: %+v", n, secret, row)
		}
	}
	// «Опасное» — последним, с вопросом
	last := doc.Components[len(doc.Components)-1]
	if last.Style != "danger" || len(last.Children) != 1 || last.Children[0].Actions[0].Confirm == "" {
		t.Errorf("«Опасное» не последнее или без вопроса: %+v", last)
	}
	// у хранилища без проверки сказано, что она будет делать
	one, err := s.buildScreen(storageAddr("диск"), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if c := findRow(one, "storage-check"); c == nil || !strings.Contains(c.Caption, "ещё не проверялось") {
		t.Errorf("непроверенное хранилище: %+v", c)
	}
	// местное: счёт файлов по его диску, проверка не нужна, убрать нельзя
	local, err := s.buildScreen(storageAddr("local"), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if findRow(local, "storage-check") != nil {
		t.Error("у местного хранилища строка про проверку")
	}
	if findRow(local, "storage-undeletable") == nil || findRow(local, "danger-remove") != nil {
		t.Error("местное хранилище должно объяснять, что не убирается, и не предлагать «Убрать»")
	}
	if _, err := s.execute(actionRequest{Action: "delete_storage", Target: "local"}); err == nil {
		t.Error("местное хранилище удалилось")
	}
	if _, ok := s.storageByName("local"); !ok {
		t.Error("местное хранилище пропало из справочника")
	}
}

// Параметры хранилища — поля экрана: сохраняются действием `save_storage`, движок
// видит новое значение, прежняя проверка сбрасывается.
func TestStorageParamsSaveByFields(t *testing.T) {
	s, st := stnFix(t)
	conf := func(name string) storage.S3Config {
		c, _ := s.storageByName(name)
		var out storage.S3Config
		_ = json.Unmarshal([]byte(c.Config), &out)
		return out
	}
	save := func(name string, params map[string]string) error {
		_, err := s.execute(actionRequest{Action: "save_storage", Target: name, Params: params})
		return err
	}
	// поля на экране — те самые, что читает движок
	doc, _ := s.buildScreen(storageAddr("yandex"), "", "", 0)
	form := formIn(doc.Components, "storage-params")
	if form == nil || form.Submit == nil || form.Submit.Action != "save_storage" || !form.Deferred {
		t.Fatalf("форма параметров не сохраняется полосой: %+v", form)
	}
	fields := map[string]Field{}
	for _, f := range form.Fields {
		fields[f.Name] = f
	}
	if fields["endpoint"].Value != "storage.yandexcloud.net" || fields["bucket"].Value != "sema-files" ||
		fields["access_secret"].Value != "s3-access" || fields["secret_secret"].Type != "picker" || fields["ssl"].Value != "true" {
		t.Errorf("поля показывают не то, что в справочнике: %+v", fields)
	}
	// в разметке нет значений ключей
	if text := stnText(t, s, storageAddr("yandex")); strings.Contains(text, stnAccessSentinel) || strings.Contains(text, stnSecretSentinel) {
		t.Error("значение ключа попало в разметку экрана хранилища")
	}

	// правка полей: файлы в зеркало ещё не уезжали — адрес менять можно
	_ = st.SetFileMirror(1, "")
	if err := save("yandex", map[string]string{"endpoint": "s3.example.org", "bucket": "новый", "region": "eu", "prefix": "/pre/", "ssl": "false",
		"access_secret": "s3-secret", "secret_secret": "s3-access"}); err != nil {
		t.Fatal(err)
	}
	got := conf("yandex")
	if got.Endpoint != "s3.example.org" || got.Bucket != "новый" || got.Region != "eu" || got.Prefix != "pre" || got.SSL ||
		got.AccessSecret != "s3-secret" || got.SecretSecret != "s3-access" {
		t.Errorf("записано не то, что вписано: %+v", got)
	}
	if note := st.StorageNote("yandex"); note != "" {
		t.Errorf("прежняя проверка относилась к прежним параметрам, а осталась %q", note)
	}
	// непришедшее поле — «не трогали»
	if err := save("yandex", map[string]string{"bucket": "ещё"}); err != nil {
		t.Fatal(err)
	}
	if got := conf("yandex"); got.Bucket != "ещё" || got.Endpoint != "s3.example.org" || got.AccessSecret != "s3-secret" {
		t.Errorf("непришедшее поле что-то сбросило: %+v", got)
	}
	// плохое отвергается словами и ничего не пишет
	for name, p := range map[string]map[string]string{
		"пустой адрес":       {"endpoint": " "},
		"пустой bucket":      {"bucket": ""},
		"секрета нет":        {"access_secret": "нет-такого"},
		"секрет не выбран":   {"secret_secret": noSecretPick},
		"половина не прошла": {"bucket": "хороший", "endpoint": ""},
	} {
		if err := save("yandex", p); err == nil {
			t.Errorf("%s: принято %v", name, p)
		}
	}
	if got := conf("yandex"); got.Bucket != "ещё" {
		t.Errorf("отвергнутая форма что-то записала: %+v", got)
	}
	// файлы уже уехали по этому адресу — место зеркала не правится
	_ = st.SetFileMirror(1, "yandex")
	if err := save("yandex", map[string]string{"bucket": "другой"}); err == nil || !strings.Contains(err.Error(), "уже лежит") {
		t.Errorf("перенос зеркала с файлами принят: %v", err)
	}
	// а ключи и HTTPS — можно
	if err := save("yandex", map[string]string{"ssl": "true", "access_secret": "s3-access"}); err != nil {
		t.Errorf("ключи у зеркала с файлами не правятся: %v", err)
	}
	// то же значение — не правка
	before, _ := s.storageByName("yandex")
	if err := save("yandex", map[string]string{"bucket": "ещё"}); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.storageByName("yandex"); after.Config != before.Config {
		t.Error("сохранение без правки переписало запись")
	}
	// Яндекс.Диск: папка и токен; токен пустым не бывает
	if err := save("диск", map[string]string{"folder": "/Копии/", "token_secret": "disk-token"}); err != nil {
		t.Fatal(err)
	}
	disk, _ := s.storageByName("диск")
	var dc engine.YaDiskConf
	_ = json.Unmarshal([]byte(disk.Config), &dc)
	if dc.Folder != "Копии" || dc.Token != "disk-token" {
		t.Errorf("Диск после сохранения: %+v", dc)
	}
	if err := save("диск", map[string]string{"token_secret": noSecretPick}); err == nil {
		t.Error("Диск остался без токена")
	}
	// у незнакомого вида и у местного параметров нет
	for _, name := range []string{"старый-ftp", "local"} {
		if err := save(name, map[string]string{"bucket": "x"}); err == nil {
			t.Errorf("%s: параметры приняты", name)
		}
	}
	if err := save("нет-такого", map[string]string{"bucket": "x"}); err == nil {
		t.Error("несуществующее хранилище приняло параметры")
	}
}

// «Добавить хранилище» — свой экран: S3 и Диск вкладками, после «Добавить» — проверка и
// экран нового хранилища. Проверка подменена: настоящая ходит в сеть.
func TestStorageAddScreens(t *testing.T) {
	s, st := stnFix(t)
	var checked []string
	s.checkStorageVia = func(name string) (string, error) {
		checked = append(checked, name)
		if name == "плохой" {
			return "", fmt.Errorf("соединиться не вышло")
		}
		return "bucket есть, запись и чтение работают", nil
	}
	// обе формы — у каждой своя кнопка, и они разные
	s3doc, _ := s.buildScreen(entityAddr(storagesScreen, storNew, ""), "", "", 0)
	diskDoc, _ := s.buildScreen(entityAddr(storagesScreen, storNew, storNewDisk), "", "", 0)
	f3, fd := formIn(s3doc.Components, "storage-new-s3"), formIn(diskDoc.Components, "storage-new-yadisk")
	if f3 == nil || f3.Submit.Action != "add_storage" || fd == nil || fd.Submit.Action != "add_yadisk" {
		t.Fatalf("формы добавления: %+v %+v", f3, fd)
	}
	if len(s3doc.Subtabs) != 2 || s3doc.Subtabs[1].ID != entityAddr(storagesScreen, storNew, storNewDisk) {
		t.Errorf("вид хранилища — вкладки: %+v", s3doc.Subtabs)
	}
	more := 0
	for _, f := range f3.Fields {
		if f.More {
			more++
		}
	}
	if more != 3 {
		t.Errorf("редких полей S3 под «Ещё» %d, ждали регион, префикс и HTTPS", more)
	}

	// S3: пресет подставляет адрес, секреты выбираются, итог — экран хранилища
	next, err := s.execute(actionRequest{Action: "add_storage", Params: map[string]string{"name": "minio", "preset": "MinIO на этой машине",
		"bucket": "sema", "access_secret": "s3-access", "secret_secret": "s3-secret", "ssl": "false", "region": "", "prefix": "sema"}})
	if err != nil {
		t.Fatal(err)
	}
	if next != storageAddr("minio") {
		t.Errorf("после добавления ведут на %q, а экран хранилища — %q", next, storageAddr("minio"))
	}
	c, ok := s.storageByName("minio")
	var conf storage.S3Config
	_ = json.Unmarshal([]byte(c.Config), &conf)
	if !ok || c.Kind != "s3" || conf.Endpoint != "127.0.0.1:9000" || conf.SSL || conf.AccessSecret != "s3-access" {
		t.Errorf("запись нового хранилища: %+v %+v", c, conf)
	}
	if len(checked) != 1 || checked[0] != "minio" {
		t.Errorf("проверка после добавления: %v", checked)
	}
	// итог проверки лежит на экране хранилища
	one, _ := s.buildScreen(storageAddr("minio"), "", "", 0)
	if row := findRow(one, "storage-check"); row == nil || !strings.Contains(row.Caption, "запись и чтение работают") {
		t.Errorf("итог проверки на экране нового хранилища: %+v", row)
	}
	// отказ проверки — не отказ добавления: хранилище заведено, ошибка видна на его экране
	if _, err := s.execute(actionRequest{Action: "add_yadisk", Params: map[string]string{"name": "плохой", "token_secret": "disk-token", "folder": "/Сёма/"}}); err != nil {
		t.Fatal(err)
	}
	bad, _ := s.buildScreen(storageAddr("плохой"), "", "", 0)
	if row := findRow(bad, "storage-check"); row == nil || row.Level != LevelAlert || !strings.Contains(row.Caption, "соединиться не вышло") {
		t.Errorf("ошибка проверки нового хранилища: %+v", row)
	}
	// отвергается: имя, без которого экран не откроется; пустое; без ключей; без токена
	for name, p := range map[string]map[string]string{
		"имя new":          {"name": "new", "endpoint": "a", "bucket": "b", "access_secret": "s3-access", "secret_secret": "s3-secret"},
		"имя с тильдой":    {"name": "a~b", "endpoint": "a", "bucket": "b", "access_secret": "s3-access", "secret_secret": "s3-secret"},
		"имя local":        {"name": "local", "endpoint": "a", "bucket": "b", "access_secret": "s3-access", "secret_secret": "s3-secret"},
		"пустое имя":       {"name": " ", "endpoint": "a", "bucket": "b", "access_secret": "s3-access", "secret_secret": "s3-secret"},
		"без bucket":       {"name": "x1", "endpoint": "a", "access_secret": "s3-access", "secret_secret": "s3-secret"},
		"секрет не выбран": {"name": "x2", "endpoint": "a", "bucket": "b", "access_secret": noSecretPick, "secret_secret": "s3-secret"},
	} {
		if _, err := s.execute(actionRequest{Action: "add_storage", Params: p}); err == nil {
			t.Errorf("S3, %s: принято", name)
		}
	}
	for name, p := range map[string]map[string]string{
		"имя new":          {"name": "new", "token_secret": "disk-token"},
		"токен не выбран":  {"name": "x3", "token_secret": noSecretPick, "folder": "a"},
		"токен не названо": {"name": "x4", "folder": "a"},
	} {
		if _, err := s.execute(actionRequest{Action: "add_yadisk", Params: p}); err == nil {
			t.Errorf("Диск, %s: принято", name)
		}
	}
	for _, name := range []string{"new", "a~b", "x1", "x2", "x3", "x4"} {
		if _, ok := s.storageByName(name); ok {
			t.Errorf("отвергнутое хранилище %q всё же завелось", name)
		}
	}
	_ = st
}

// Прежние действия живут под прежними именами: на них смотрят скрипты и уведомления.
func TestStorageOldActionsStillWork(t *testing.T) {
	s, st := stnFix(t)
	// «Токен Диска» с вопросом: имя секрета в value; пустое — Диск перестаёт работать
	if _, err := s.execute(actionRequest{Action: "set_yadisk_token", Target: "диск", Params: map[string]string{"value": "s3-secret"}}); err != nil {
		t.Fatal(err)
	}
	c, _ := s.storageByName("диск")
	if !strings.Contains(c.Config, `"token_secret":"s3-secret"`) {
		t.Errorf("токен после set_yadisk_token: %s", c.Config)
	}
	if _, err := s.execute(actionRequest{Action: "set_yadisk_token", Target: "диск", Params: map[string]string{"value": "нет-такого"}}); err == nil {
		t.Error("токен-секрет, которого нет, принят")
	}
	if _, err := s.execute(actionRequest{Action: "set_yadisk_token", Target: "yandex", Params: map[string]string{"value": "s3-secret"}}); err == nil {
		t.Error("токен Диска записан хранилищу другого вида")
	}
	// «Проверить» остаётся на месте, «Убрать» — тоже, и ведёт на список
	var asked []string
	s.checkStorageVia = func(name string) (string, error) { asked = append(asked, name); return "работает", nil }
	if next, err := s.execute(actionRequest{Action: "check_storage", Target: "диск"}); err != nil || next != "" || len(asked) != 1 {
		t.Errorf("check_storage: %q %v %v", next, err, asked)
	}
	next, err := s.execute(actionRequest{Action: "delete_storage", Target: "диск"})
	if err != nil || next != storagesScreen {
		t.Errorf("delete_storage вернул %q, %v: с экрана убранного хранилища — на список", next, err)
	}
	if _, ok := s.storageByName("диск"); ok {
		t.Error("хранилище осталось после delete_storage")
	}
	// убранное зеркало не оставляет файлам пометку «в зеркале»: они снова ждут
	_ = st.SetFileMirror(2, "yandex")
	if _, err := s.execute(actionRequest{Action: "delete_storage", Target: "yandex"}); err != nil {
		t.Fatal(err)
	}
	if st.FileMirror(2) != "" || st.FileMirror(1) != "" {
		t.Error("после «Убрать» файлы остались отмеченными как лежащие в убранном зеркале")
	}
	// старый адрес с вкладкой открывает раздел, а незнакомого хранилища нет — объяснение
	for _, screen := range []string{"settings:storages~sources", "settings:storages~нечто", "settings:storages@нет-такого"} {
		if _, err := s.buildScreen(screen, "", "", 0); err != nil {
			t.Errorf("%s: %v", screen, err)
		}
	}
	gone, _ := s.buildScreen("settings:storages@нет-такого", "", "", 0)
	if e := gone.Components[len(gone.Components)-1]; e.Style != "empty" || !strings.Contains(e.Subtitle, "нет") {
		t.Errorf("экран несуществующего хранилища: %+v", e)
	}
}

// Наблюдаемые папки: тонкие строки с «Настройками» в раскрытии; «удалять после
// приёма» — поле формы, и сохраняет его полоса; прежние действия остались.
func TestFolderRowsEditByFields(t *testing.T) {
	s, st := stnFix(t)
	doc, err := s.buildScreen(storagesScreen+"~"+storTabSources, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	folders, _ := st.FileFolders()
	var scans, inbox *Component
	for _, f := range folders {
		id := strconv.FormatInt(f.ID, 10)
		row := findRow(doc, "folder-"+id)
		if row == nil {
			t.Fatalf("строки папки %s нет", f.Path)
		}
		if f.Path == "/data/scans" {
			scans = row
		} else {
			inbox = row
		}
		if !row.Expandable || row.Expand != "Настройки" || len(row.Children) != 1 {
			t.Errorf("%s: «Настройки» не раскрываются на месте: %+v", f.Path, row)
		}
		if n := stnVisibleButtons(row); n != 0 {
			t.Errorf("%s: в строке кнопок %d, а действия — тумблер и «···»", f.Path, n)
		}
		if row.Toggle == "" || len(row.Menu) != 1 || row.Menu[0].Action != "delete_folder_source" || row.Menu[0].Confirm == "" {
			t.Errorf("%s: тумблер/меню: %+v", f.Path, row)
		}
	}
	if scans == nil || inbox == nil {
		t.Fatal("нет обеих папок")
	}
	if len(scans.Badges) != 1 || scans.Badges[0].Text != "ошибка" || !strings.Contains(scans.Caption, "permission denied") || scans.Level != LevelAlert {
		t.Errorf("ошибка папки не видна: %+v", scans)
	}
	if inbox.Badges[0].Text != "выключена" {
		t.Errorf("выключенная папка: %+v", inbox.Badges)
	}
	// форма: «Удалять файлы после приёма» с подсказкой про необратимое, значение — как в записи
	form := scans.Children[0]
	if form.Type != "form" || form.Submit.Action != "save_folder" || len(form.Fields) != 1 {
		t.Fatalf("форма папки: %+v", form)
	}
	f := form.Fields[0]
	if f.Type != "toggle" || f.Value != "true" || !strings.Contains(f.Hint, "Необратимо") || !strings.Contains(f.Hint, "принят без ошибки") {
		t.Errorf("поле «удалять после приёма»: %+v", f)
	}
	// сохраняется полосой: форма отложена
	if !form.Deferred {
		t.Error("форму папки не сохраняет нижняя полоса")
	}
	// снятый тумблер приходит как false; непришедшее поле — не трогали
	id := strconv.FormatInt(folders[0].ID, 10)
	for _, f := range folders {
		if f.Path == "/data/scans" {
			id = strconv.FormatInt(f.ID, 10)
		}
	}
	if _, err := s.execute(actionRequest{Action: "save_folder", Target: id, Params: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.FileFolders(); !list[1].DeleteAfter && list[1].Path == "/data/scans" {
		t.Error("save_folder без поля сбросил «удалять после приёма»")
	}
	if _, err := s.execute(actionRequest{Action: "save_folder", Target: id, Params: map[string]string{"delete_after": "false"}}); err != nil {
		t.Fatal(err)
	}
	for _, fd := range mustFolders(t, st) {
		if fd.Path == "/data/scans" && fd.DeleteAfter {
			t.Error("save_folder с false не снял «удалять после приёма»")
		}
	}
	if _, err := s.execute(actionRequest{Action: "save_folder", Target: id, Params: map[string]string{"delete_after": "true"}}); err != nil {
		t.Fatal(err)
	}
	for _, fd := range mustFolders(t, st) {
		if fd.Path == "/data/scans" && !fd.DeleteAfter {
			t.Error("save_folder с true не включил «удалять после приёма»")
		}
	}
	if _, err := s.execute(actionRequest{Action: "save_folder", Target: "9999", Params: map[string]string{"delete_after": "true"}}); err == nil {
		t.Error("несуществующая папка приняла настройки")
	}
	// прежние действия: тумблер, кнопка «удалять» и «Убрать»; новая папка — своя форма с кнопкой
	if _, err := s.execute(actionRequest{Action: "toggle_folder_delete", Target: id}); err != nil {
		t.Fatal(err)
	}
	for _, fd := range mustFolders(t, st) {
		if fd.Path == "/data/scans" && fd.DeleteAfter {
			t.Error("toggle_folder_delete не переключил")
		}
	}
	if _, err := s.execute(actionRequest{Action: "toggle_folder_source", Target: id}); err != nil {
		t.Fatal(err)
	}
	add := formIn(doc.Components, "add-folder")
	if add == nil || add.Submit.Action != "add_folder_source" || add.Deferred {
		t.Fatalf("форма «Новая папка»: %+v", add)
	}
	if _, err := s.execute(actionRequest{Action: "add_folder_source", Params: map[string]string{"path": "относительная"}}); err == nil {
		t.Error("относительный путь принят")
	}
	if _, err := s.execute(actionRequest{Action: "add_folder_source", Params: map[string]string{"path": "/data/new", "delete_after": "false"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute(actionRequest{Action: "delete_folder_source", Target: id}); err != nil {
		t.Fatal(err)
	}
	if n := len(mustFolders(t, st)); n != 2 {
		t.Errorf("после добавления и «Убрать» папок %d, ждали 2", n)
	}
}

func mustFolders(t *testing.T, st *store.Store) []*store.FileFolder {
	t.Helper()
	list, err := st.FileFolders()
	if err != nil {
		t.Fatal(err)
	}
	return list
}
