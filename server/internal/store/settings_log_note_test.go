package store

// Что изменилось в составной настройке: человеческие слова вместо ключа и обрезка
// JSON. Живой журнал 2 октября 2026 показывал «models.json» и обрывок скобки;
// эти тесты держат два обещания: слова верные и ничего секретного в них нет.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PavluninVladimir/sema/server/internal/secret"
)

// noteOf — описание правки по двум значениям составной настройки.
func noteOf(t *testing.T, st *Store, key, before, after string) (ChangeNote, bool) {
	t.Helper()
	note, service, ok := st.describeChange(key, before, after, false)
	if !ok {
		t.Fatalf("%s: настройка не опознана как составная", key)
	}
	return note, service
}

func TestNoteModelPriceChanged(t *testing.T) {
	st := testStore(t)
	before := `[{"name":"gpt-6-luna","price":"$0.55 / 1M","type":"openai"},{"name":"qwen3","price":"0"}]`
	after := `[{"name":"gpt-6-luna","price":"$0.60 / 1M","price_by":"прайс","price_at":"2026-10-02","type":"openai"},{"name":"qwen3","price":"0"}]`
	note, service := noteOf(t, st, "models.json", before, after)
	if service {
		t.Errorf("правка цены названа служебной")
	}
	if note.Title != "Модели: у gpt-6-luna изменена цена" {
		t.Errorf("название: %q", note.Title)
	}
	if note.Was != "$0.55 / 1M" || note.Now != "$0.60 / 1M" {
		t.Errorf("было → стало: %q → %q", note.Was, note.Now)
	}
	if note.Kind != NoteModels || note.Ref != "gpt-6-luna" || note.Count != 1 || note.Gone {
		t.Errorf("куда вести: %+v", note)
	}
}

func TestNoteModelAddedAndRemoved(t *testing.T) {
	st := testStore(t)
	base := `[{"name":"a"}]`
	note, _ := noteOf(t, st, "models.json", base, `[{"name":"a"},{"name":"qwen3"}]`)
	if note.Title != "Модели: подключена qwen3" || note.Ref != "qwen3" {
		t.Errorf("одна подключена: %+v", note)
	}
	note, _ = noteOf(t, st, "models.json", `[{"name":"a"},{"name":"qwen3"}]`, base)
	if note.Title != "Модели: убрана qwen3" || !note.Gone {
		t.Errorf("одна убрана: %+v", note)
	}
	note, _ = noteOf(t, st, "models.json", base, `[{"name":"a"},{"name":"m1"},{"name":"m2"},{"name":"m3"},{"name":"m4"}]`)
	if note.Title != "Модели: подключено 4 модели: m1, m2, m3 и ещё 1" || note.Count != 4 {
		t.Errorf("несколько подключены: %+v", note)
	}
	note, _ = noteOf(t, st, "models.json", `[{"name":"a"},{"name":"b"},{"name":"c"},{"name":"d"},{"name":"e"}]`, `[{"name":"a"}]`)
	if note.Title != "Модели: убрано 4 модели: b, c, d и ещё 1" {
		t.Errorf("несколько убраны: %+v", note)
	}
}

func TestNoteRoleModelChanged(t *testing.T) {
	st := testStore(t)
	before := `[{"name":"Чаты","model":"gpt-6-luna","spare":"нет","timeout":120}]`
	after := `[{"name":"Чаты","model":"DeepseekPro","spare":"нет","timeout":120}]`
	note, _ := noteOf(t, st, "roles.json", before, after)
	if note.Title != "Роли: «Чаты» — модель DeepseekPro вместо gpt-6-luna" {
		t.Errorf("название: %q", note.Title)
	}
	if note.Was != "gpt-6-luna" || note.Now != "DeepseekPro" {
		t.Errorf("было → стало: %q → %q", note.Was, note.Now)
	}
}

// Результат последнего вызова роли пишется на каждом ответе модели: правкой это не
// считается, даже если вышло в ту же секунду, что и действие человека.
func TestNoteRoleResultIsService(t *testing.T) {
	st := testStore(t)
	before := `[{"name":"Чаты","model":"a","last":"основной","last_err":""}]`
	after := `[{"name":"Чаты","model":"a","last":"запасная","last_err":"01.10 · не ответила"}]`
	note, service := noteOf(t, st, "roles.json", before, after)
	if !service {
		t.Errorf("служебное обновление роли названо правкой: %+v", note)
	}
}

func TestNoteModuleEnabled(t *testing.T) {
	st := testStore(t)
	before := `{"receipts":{"name":"receipts","title":"Чеки","enabled":false}}`
	after := `{"receipts":{"name":"receipts","title":"Чеки","enabled":true}}`
	note, _ := noteOf(t, st, "modules.json", before, after)
	if note.Title != "Модули: «Чеки» включён" {
		t.Errorf("включён: %q", note.Title)
	}
	note, _ = noteOf(t, st, "modules.json", after, before)
	if note.Title != "Модули: «Чеки» выключен" {
		t.Errorf("выключен: %q", note.Title)
	}
	// ломается модуль сам: причина и история поломок правкой человека не считаются
	broken := `{"receipts":{"name":"receipts","title":"Чеки","enabled":true,"broken":"упал","breaks":[{"at":"x"}]}}`
	if _, service := noteOf(t, st, "modules.json", after, broken); !service {
		t.Errorf("поломка модуля названа правкой человека")
	}
}

func TestNoteMailboxSetting(t *testing.T) {
	st := testStore(t)
	before := `{"me@example.com":{"auth":"password","junk_days":14,"connected":"2026-10-01T10:00:00Z"}}`
	after := `{"me@example.com":{"auth":"password","junk_days":30,"connected":"2026-10-02T10:00:00Z"}}`
	note, _ := noteOf(t, st, "mailconf.json", before, after)
	if note.Title != "Почта: ящик me@example.com — срок спама 30" {
		t.Errorf("название: %q", note.Title)
	}
	if note.Was != "14" || note.Now != "30" || note.Kind != NoteMail || note.Ref != "me@example.com" {
		t.Errorf("описание: %+v", note)
	}
	// вход удался, список папок прочитан — не правка
	connect := `{"me@example.com":{"auth":"password","junk_days":30,"connected":"2026-10-03T10:00:00Z","folders":["INBOX","Спам"]}}`
	if _, service := noteOf(t, st, "mailconf.json", after, connect); !service {
		t.Errorf("вход в ящик назван правкой")
	}
}

func TestNoteManyChangesAtOnce(t *testing.T) {
	st := testStore(t)
	before := `[{"name":"a","price":"1"},{"name":"b","price":"2"},{"name":"c","price":"3"}]`
	after := `[{"name":"a","price":"1.5"},{"name":"b","price":"2.5"},{"name":"c","price":"3.5"}]`
	note, _ := noteOf(t, st, "models.json", before, after)
	if note.Title != "Модели: изменено «цена» — 3 модели: a, b, c" || note.Count != 3 || note.Was != "" {
		t.Errorf("одно поле у всех: %+v", note)
	}
	mixed := `[{"name":"a","price":"1.5","vision":true},{"name":"b","price":"2"},{"name":"c","price":"3"}]`
	note, _ = noteOf(t, st, "models.json", before, mixed)
	if note.Title != "Модели: у a изменено: цена, видит картинки" || note.Count != 1 {
		t.Errorf("несколько полей у одной: %q", note.Title)
	}
	wide := `[{"name":"a","price":"9","vision":true},{"name":"b","price":"2","trusted":true},{"name":"c","price":"3"}]`
	note, _ = noteOf(t, st, "models.json", before, wide)
	if note.Title != "Модели: изменён список (2 модели): a, b" || note.Count != 2 {
		t.Errorf("разные поля у разных: %q", note.Title)
	}
}

func TestNoteUnreadableValue(t *testing.T) {
	st := testStore(t)
	note, _ := noteOf(t, st, "models.json", `[{"name":"a"}]`, `[{"name":"a"`)
	if note.Title != "Модели: изменён список" {
		t.Errorf("не JSON: %q", note.Title)
	}
	if _, _, ok := st.describeChange("ui.poll_s", "4", "5", false); ok {
		t.Errorf("простая настройка названа составной")
	}
}

// Ключ, пароль, токен и всё, чего нет в описании поля, не попадает в запись ни
// значением, ни обрезком: об изменении сказано, о содержимом — нет.
func TestNoteNeverShowsSecretValues(t *testing.T) {
	st := testStore(t)
	const was, now = "СЕКРЕТ-СТАРЫЙ-9f2a", "СЕКРЕТ-НОВЫЙ-71c4"
	for _, key := range NoteKeys() {
		sp := noteSpecs[key]
		for name, f := range sp.Fields {
			if f.Kind != noteSecret && f.Kind != noteBare {
				continue
			}
			mk := func(v string) string {
				ent := map[string]any{"name": "x", name: v}
				if sp.Map {
					raw, _ := json.Marshal(map[string]any{"x": ent})
					return string(raw)
				}
				raw, _ := json.Marshal([]any{ent})
				return string(raw)
			}
			note, _, _ := st.describeChange(key, mk(was), mk(now), false)
			shown := note.Title + "|" + note.Was + "|" + note.Now
			if strings.Contains(shown, "СЕКРЕТ") {
				t.Errorf("%s.%s: значение попало в описание правки: %q", key, name, shown)
			}
			if !strings.Contains(note.Title, f.Label) && len(sp.Fields) > 0 {
				t.Errorf("%s.%s: об изменении не сказано: %q", key, name, note.Title)
			}
		}
		// поле, которого в описании нет, — тоже без значения
		mk := func(v string) string {
			ent := map[string]any{"name": "x", "novoe_pole": v}
			if sp.Map {
				raw, _ := json.Marshal(map[string]any{"x": ent})
				return string(raw)
			}
			raw, _ := json.Marshal([]any{ent})
			return string(raw)
		}
		note, _, _ := st.describeChange(key, mk(was), mk(now), false)
		if strings.Contains(note.Title+note.Was+note.Now, "СЕКРЕТ") {
			t.Errorf("%s: неописанное поле показало значение: %+v", key, note)
		}
	}
	// имя поля говорит о секрете — оно обязано быть закрытым или служебным
	for _, key := range NoteKeys() {
		for name, f := range noteSpecs[key].Fields {
			// флаг и число секретом быть не могут: «нужен ключ» — да или нет
			if nameSmellsSecret(name) && f.Kind != noteSecret && f.Kind != noteIgnore && f.Kind != noteBare &&
				f.Kind != noteBool && f.Kind != noteNum {
				t.Errorf("%s.%s: имя говорит о секрете, а поле показывается (вид %d)", key, name, f.Kind)
			}
		}
	}
}

// Ключ провайдера лежит зашифрованным, и каждое сохранение даёт другой шифр: если бы
// сравнение шло по тексту, «ключ изменён» стояло бы в каждой правке списка.
func TestNoteSecretComparedDecrypted(t *testing.T) {
	st := testStore(t)
	st.UseSecretBox(mustBox(t))
	one, _ := st.box.Seal("sk-test-1")
	again, _ := st.box.Seal("sk-test-1")
	other, _ := st.box.Seal("sk-test-2")
	if one == again {
		t.Fatal("шифр не должен повторяться: проверка бессмысленна")
	}
	mk := func(key string) string { return `[{"name":"m","api_key":"` + key + `"}]` }
	if _, service := noteOf(t, st, "models.json", mk(one), mk(again)); !service {
		t.Errorf("пересохранение того же ключа названо правкой")
	}
	note, service := noteOf(t, st, "models.json", mk(one), mk(other))
	if service || note.Title != "Модели: у m изменён ключ" || note.Was != "" || note.Now != "" {
		t.Errorf("смена ключа: %+v (служебное %v)", note, service)
	}
	for _, v := range []string{"sk-test-1", "sk-test-2", one, other} {
		if strings.Contains(note.Title+note.Was+note.Now, v) {
			t.Errorf("значение ключа в описании: %q", v)
		}
	}
}

// Дверь пишет описание сама, а отбор журнала учитывает служебность: правка цены —
// в «Ваших», обновление результата роли — в «Служебных», и ничего не потеряно.
func TestNoteWrittenByDoorAndFiltered(t *testing.T) {
	st := testStore(t)
	human(t, st, "models.json", `[{"name":"gpt-6-luna","price":"1"}]`)
	human(t, st, "models.json", `[{"name":"gpt-6-luna","price":"2"}]`)
	human(t, st, "roles.json", `[{"name":"Чаты","model":"a","last":"основной"}]`)
	human(t, st, "roles.json", `[{"name":"Чаты","model":"a","last":"запасная"}]`)

	mine, err := st.SettingsLogView(SettingsLogMine, 50)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, c := range mine {
		if c.Note == nil {
			t.Fatalf("%s: описания нет", c.Key)
		}
		titles = append(titles, c.Note.Title)
	}
	got := strings.Join(titles, " | ")
	if !strings.Contains(got, "Модели: у gpt-6-luna изменена цена") || !strings.Contains(got, "Роли: добавлена роль «Чаты»") {
		t.Errorf("«Ваши правки»: %s", got)
	}
	if strings.Contains(got, "служебное") {
		t.Errorf("служебное обновление попало в «Ваши»: %s", got)
	}
	service, _ := st.SettingsLogView(SettingsLogService, 50)
	found := false
	for _, c := range service {
		if c.Note != nil && strings.Contains(c.Note.Title, "служебное обновление") {
			found = true
		}
	}
	if !found {
		t.Errorf("служебное обновление роли не нашлось среди служебных")
	}
	all, _ := st.SettingsLogView(SettingsLogAll, 100)
	nMine, nService, nTotal := st.CountSettingsLogView()
	if nMine+nService != nTotal || nTotal != len(all) {
		t.Errorf("записи потерялись: ваших %d + служебных %d != %d (в журнале %d)", nMine, nService, nTotal, len(all))
	}
	// «Под рукой» не считает служебное
	keys, _ := st.TopEditedKeys("2000-01-01T00:00:00Z", 10)
	for _, k := range keys {
		if k.Key == "roles.json" && k.Count > 1 {
			t.Errorf("служебные обновления роли посчитаны как правки: %+v", k)
		}
	}
}

// Запись, легшая до описания правок, читается обобщённой строкой: значения в журнале
// обрезаны, и из них ничего не восстановить.
func TestNoteFallbackForOldRows(t *testing.T) {
	st := testStore(t)
	if _, err := st.db.Exec(`INSERT INTO settings_log (at, key, old_val, new_val, who, source)
		VALUES ('2026-10-01T10:00:00Z', 'models.json', '[{"name":"a","pri', '[{"name":"a","pri', 'вы', 'settings:models · x')`); err != nil {
		t.Fatal(err)
	}
	list, _ := st.SettingsLogView(SettingsLogMine, 10)
	if len(list) == 0 || list[0].Note == nil || list[0].Note.Title != "Модели: изменён список" {
		t.Errorf("старая запись: %+v", list)
	}
}

// Длинное и русское режется по рунам: ни одной разбитой буквы, ни одной строки
// длиннее предела.
func TestNoteLengthIsBoundedByRunes(t *testing.T) {
	st := testStore(t)
	long := strings.Repeat("Ё", 400)
	before := fmt.Sprintf(`[{"name":%q,"price":""}]`, strings.Repeat("Ж", 250))
	after := fmt.Sprintf(`[{"name":%q,"price":%q}]`, strings.Repeat("Ж", 250), long)
	note, _ := noteOf(t, st, "models.json", before, after)
	for _, s := range []string{note.Title, note.Was, note.Now} {
		if !utf8.ValidString(s) {
			t.Errorf("разбитая руна: %q", s)
		}
	}
	if utf8.RuneCountInString(note.Title) > noteTitleMax || utf8.RuneCountInString(note.Now) > noteValueMax {
		t.Errorf("длиннее предела: заголовок %d, значение %d", utf8.RuneCountInString(note.Title), utf8.RuneCountInString(note.Now))
	}
	names := make([]string, 0, 60)
	var list []string
	for i := 0; i < 60; i++ {
		n := fmt.Sprintf("модель-%d-%s", i, strings.Repeat("я", 30))
		names = append(names, n)
		list = append(list, fmt.Sprintf(`{"name":%q}`, n))
	}
	note, _ = noteOf(t, st, "models.json", `[]`, "["+strings.Join(list, ",")+"]")
	if utf8.RuneCountInString(note.Title) > noteTitleMax || !utf8.ValidString(note.Title) {
		t.Errorf("заголовок на много моделей: %d рун", utf8.RuneCountInString(note.Title))
	}
	if note.Count != 60 {
		t.Errorf("счёт: %d", note.Count)
	}
}

// Описание правки считается на каждой записи составной настройки, а список моделей —
// сто семьдесят три строки, модуль — сотни килобайт кода: счёт не должен заметно
// удлинять запись.
func TestNoteCostOnBigLists(t *testing.T) {
	st := testStore(t)
	var before, after []string
	for i := 0; i < 173; i++ {
		before = append(before, fmt.Sprintf(`{"name":"model-%d","price":"$0.%02d / 1M","context":128000,"type":"openai","base_url":"https://api.example.com/v1","model":"vendor/model-%d","api_key":"enc:v1:%s"}`, i, i%90, i, strings.Repeat("A", 80)))
		price := i % 90
		if i == 100 {
			price = 91
		}
		after = append(after, fmt.Sprintf(`{"name":"model-%d","price":"$0.%02d / 1M","context":128000,"type":"openai","base_url":"https://api.example.com/v1","model":"vendor/model-%d","api_key":"enc:v1:%s"}`, i, price, i, strings.Repeat("A", 80)))
	}
	start := time.Now()
	note, _ := noteOf(t, st, "models.json", "["+strings.Join(before, ",")+"]", "["+strings.Join(after, ",")+"]")
	took := time.Since(start)
	t.Logf("173 модели: %v → %s", took, note.Title)
	if note.Title != "Модели: у model-100 изменена цена" {
		t.Errorf("название: %q", note.Title)
	}
	if took > 100*time.Millisecond {
		t.Errorf("описание правки списка моделей считается %v", took)
	}
	code := strings.Repeat("function f(){ return 1 } // строка кода модуля\n", 5000)
	mod := func(c string) string {
		raw, _ := json.Marshal(map[string]any{"big": map[string]any{"name": "big", "title": "Большой", "code": c, "enabled": true}})
		return string(raw)
	}
	start = time.Now()
	note, _ = noteOf(t, st, "modules.json", mod(code), mod(code+"// ещё строка\n"))
	took = time.Since(start)
	t.Logf("модуль на %d КБ кода: %v → %s", len(code)/1024, took, note.Title)
	if note.Title != "Модули: у модуля «Большой» изменён код" || strings.Contains(note.Was+note.Now, "function") {
		t.Errorf("код модуля: %+v", note)
	}
	if took > 100*time.Millisecond {
		t.Errorf("описание правки модуля считается %v", took)
	}
}

// Структуры, которые лежат в составных настройках, описаны полностью: новое поле
// без строки в `noteSpecs` показывалось бы человеку как «поле «x»».
func TestNoteKnowsEveryStoreField(t *testing.T) {
	for _, c := range []struct {
		key string
		typ reflect.Type
	}{
		{"mailconf.json", reflect.TypeOf(MailConf{})},
		{"accounts.json", reflect.TypeOf(Account{})},
	} {
		for _, name := range jsonFieldNames(c.typ) {
			if !NoteKnows(c.key, name) {
				t.Errorf("%s: поле %q не описано в noteSpecs (settings_log_note.go)", c.key, name)
			}
		}
	}
}

// jsonFieldNames — имена полей структуры в JSON: по тегу, а без тега — по имени.
func jsonFieldNames(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}

func mustBox(t *testing.T) *secret.Box {
	t.Helper()
	box, err := secret.New("проба-ключа-для-описания-правок")
	if err != nil {
		t.Fatalf("ключ: %v", err)
	}
	return box
}
