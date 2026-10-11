package store

// Что изменилось в составной настройке — человеческими словами.
//
// Часть настроек лежит в таблице одной строкой JSON на весь список: модели,
// роли, провайдеры, модули, настройки ящиков (`models.json`, `roles.json`,
// `providers.json`, `modules.json`, `mailconf.json`, `accounts.json`). Журналу
// такая запись давала только ключ, а значения режутся до трёхсот знаков — на
// «Вы меняли» стояло «models.json» и обрезок скобки. 2 октября 2026 живой журнал
// показал это на цифрах: 222 правки под «Вашими», и среди них такие.
//
// Дверь записи знает старое и новое значение целиком — до того, как их режут для
// журнала, — поэтому «что изменилось» считается здесь же, в момент записи, и
// ложится рядом (`settings_log.note`): «Модели: у gpt-6-luna изменена цена», «Роли:
// «Чаты» — модель DeepseekPro вместо gpt-6-luna», «Модули: «Чеки» включён».
// Сравнение идёт по сущностям: кто добавлен, кто убран, у кого и какие поля
// сменились. Много сущностей сразу — «изменён список (3 модели)».
//
// Три правила, ради которых это не «сравнить два JSON»:
//
//  1. Секреты не показываются никогда. Поле ключа, пароля, токена сравнивается
//     расшифрованным (зашифрованное каждый раз другое — иначе «ключ изменён»
//     стояло бы в каждой правке), а в записи остаётся только «ключ изменён» без
//     значения. Что не названо в описании поля, считается «без значения»: новое
//     поле структуры не вытечет в журнал само.
//  2. Служебные поля (последний результат роли, «подключён», список папок с
//     сервера) правкой не считаются. Запись, где изменилось только такое, —
//     служебная (`settings_log.svc`): человеческой подписи у неё нет по сути.
//  3. Описание полей — рядом с кодом, который их читает, и держится тестом:
//     поле структуры без строки здесь роняет сборку (`TestNoteKnowsEveryField`).

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Виды составных настроек — по ним экран находит, куда вести.
const (
	NoteModels    = "models"
	NoteRoles     = "roles"
	NoteProviders = "providers"
	NoteModules   = "modules"
	NoteMail      = "mail"
	NoteAccounts  = "accounts"
)

// ChangeNote — что изменилось в составной настройке, как это записано в журнале.
type ChangeNote struct {
	// Title — правка словами: «Модели: у gpt-6-luna изменена цена»
	Title string `json:"t"`
	// Was и Now — короткие значения «было → стало»; пусто, когда значение не
	// показывается (ключ, длинный текст) или правок несколько
	Was string `json:"w,omitempty"`
	Now string `json:"n,omitempty"`
	// Kind — какая это составная настройка (`NoteModels`…), Ref — сущность, если
	// затронута одна, Gone — её убрали (вести можно только к списку), Count —
	// сколько сущностей затронуто
	Kind  string `json:"k"`
	Ref   string `json:"r,omitempty"`
	Gone  bool   `json:"g,omitempty"`
	Count int    `json:"c,omitempty"`
}

const (
	noteTitleMax = 200 // знаков в заголовке правки
	noteValueMax = 60  // знаков в одном показанном значении
	noteNames    = 3   // сколько имён сущностей называть, остальное — «и ещё N»
)

// Виды значений поля.
const (
	noteText   = iota // строка, показывается коротко
	noteBool          // да / нет
	noteNum           // число
	noteList          // список строк
	noteMap           // словарь «имя → значение»
	noteBare          // «менялось» сказано, значение — нет: длинное или личное
	noteSecret        // ключ, пароль: сравнивается расшифрованным, не показывается никогда
	noteIgnore        // служебное: правкой не считается
)

// noteField — как одно поле называется человеку и как показывается.
type noteField struct {
	Label  string
	Kind   int
	Gender byte // 'f' цена, 'm' размер, 'n' имя, 'p' часы: «изменена / изменён / изменено / изменены»
	// Tpl — название правки одного поля у одной сущности: {ref} — сущность,
	// {old} и {new} — значения, {label} — название поля. TplNew — то же, когда
	// раньше значения не было. Пусто — «у gpt-6-luna изменена цена».
	Tpl, TplNew string
	// On и Off — как называется флаг, когда он включён и выключен
	On, Off string
	// Tri — у флага есть третье состояние «как по умолчанию» (указатель в JSON)
	Tri bool
	// Zero — как показывается пустое или нулевое число; пусто — «не задано»
	Zero string
	// Values — перевод значений для человека («tidy» → «наводить порядок»)
	Values map[string]string
}

// noteSpec — как читать составную настройку.
type noteSpec struct {
	Kind    string
	Section string // «Модели»
	Map     bool   // словарь «имя → сущность»; иначе список с полем IDField
	IDField string
	// Noun — «модель, модели, моделей» для счёта в скобках
	Noun [3]string
	// Ref — сущность в тексте («gpt-6-luna», «ящик vp@…», «Чеки» в кавычках);
	// Subject — она же с предлогом: «у gpt-6-luna»
	Ref, Subject func(id string, e map[string]any) string
	// шаблоны добавления и убирания: один — с названием, много — со счётом
	AddOne, DelOne, AddMany, DelMany string
	Fields                           map[string]noteField
}

func fText(label string, g byte) noteField { return noteField{Label: label, Kind: noteText, Gender: g} }
func fNum(label string, g byte, zero string) noteField {
	return noteField{Label: label, Kind: noteNum, Gender: g, Zero: zero}
}
func fBool(label, on, off string) noteField {
	return noteField{Label: label, Kind: noteBool, Gender: 'n', On: on, Off: off}
}
func fTri(label, on, off string) noteField {
	f := fBool(label, on, off)
	f.Tri = true
	return f
}
func fList(label string, g byte) noteField { return noteField{Label: label, Kind: noteList, Gender: g} }
func fMap(label string, g byte) noteField  { return noteField{Label: label, Kind: noteMap, Gender: g} }
func fBare(label string, g byte) noteField { return noteField{Label: label, Kind: noteBare, Gender: g} }
func fSecret(label string) noteField {
	return noteField{Label: label, Kind: noteSecret, Gender: 'm'}
}

var fIgnore = noteField{Kind: noteIgnore}

func (f noteField) with(tpl, tplNew string) noteField {
	f.Tpl, f.TplNew = tpl, tplNew
	return f
}

func (f noteField) values(v map[string]string) noteField {
	f.Values = v
	return f
}

func idRef(id string, _ map[string]any) string { return id }

func quoted(id string, e map[string]any) string {
	if t, _ := e["title"].(string); strings.TrimSpace(t) != "" {
		return "«" + strings.TrimSpace(t) + "»"
	}
	return "«" + id + "»"
}

// noteSpecs — составные настройки по ключу. Описание поля стоит рядом с
// полем: тот, кто добавляет поле в `ModelConf`, `Role`, `ProviderConf`, `Module`,
// `MailConf` или `Account`, обязан назвать его здесь — иначе тест роняет сборку.
var noteSpecs = map[string]*noteSpec{
	"models.json": {
		Kind: NoteModels, Section: "Модели", IDField: "name", Noun: [3]string{"модель", "модели", "моделей"},
		Ref: idRef, Subject: func(id string, _ map[string]any) string { return "у " + id },
		AddOne: "подключена %s", DelOne: "убрана %s", AddMany: "подключено %s", DelMany: "убрано %s",
		Fields: map[string]noteField{
			"name":       fIgnore,
			"vision":     fBool("видит картинки", "видит картинки", "не видит картинки"),
			"search":     fBool("ищет в интернете", "ищет в интернете", "не ищет в интернете"),
			"hears":      fBool("слышит", "слышит", "не слышит"),
			"trusted":    fBool("доверие", "доверена", "больше не доверена"),
			"context":    fNum("размер контекста", 'm', ""),
			"price":      fText("цена", 'f'),
			"price_peak": fText("цена в часы пик", 'f'),
			"peak":       fText("часы пик", 'p'),
			"price_name": fText("имя в прайсе", 'n'),
			"price_by":   fIgnore, // кто поставил цену: едет вместе с ценой
			"price_at":   fIgnore,
			"type":       fText("вид подключения", 'm'),
			"base_url":   fBare("адрес", 'm'),
			"model":      fText("имя в запросе", 'n'),
			"api_key":    fSecret("ключ"),
		},
	},
	"roles.json": {
		Kind: NoteRoles, Section: "Роли", IDField: "name", Noun: [3]string{"роль", "роли", "ролей"},
		Ref:     quoted,
		Subject: func(id string, e map[string]any) string { return "у роли " + quoted(id, e) },
		AddOne:  "добавлена роль %s", DelOne: "убрана роль %s", AddMany: "добавлено %s", DelMany: "убрано %s",
		Fields: map[string]noteField{
			"name":     fIgnore,
			"model":    fText("модель", 'f').with("{ref} — модель {new} вместо {old}", "{ref} — модель {new}"),
			"spare":    fText("запасная модель", 'f').with("{ref} — запасная: {new} (было: {old})", "{ref} — запасная: {new}"),
			"timeout":  fNum("время ожидания", 'n', "").with("{ref} — время ожидания {new} с (было {old})", ""),
			"last":     fIgnore, // чем получен последний результат: пишется на каждом вызове
			"last_err": fIgnore,
		},
	},
	"providers.json": {
		Kind: NoteProviders, Section: "Провайдеры", IDField: "name", Noun: [3]string{"провайдер", "провайдера", "провайдеров"},
		Ref: idRef, Subject: func(id string, _ map[string]any) string { return "у провайдера " + id },
		AddOne: "подключён провайдер %s", DelOne: "убран провайдер %s", AddMany: "подключено %s", DelMany: "убрано %s",
		Fields: map[string]noteField{
			"name":         fIgnore,
			"kind":         fText("вид подключения", 'm'),
			"base_url":     fBare("адрес", 'm'),
			"hint":         fBare("подсказка", 'f'),
			"need_key":     fBool("нужен ключ", "требует ключ", "не требует ключ"),
			"catalog":      fBool("список моделей", "отдаёт список моделей", "не отдаёт список моделей"),
			"price_kind":   fText("источник цен", 'm').values(map[string]string{"api": "JSON с ценами", "page": "страница цен", "reported": "называет сам", "none": "нет"}),
			"price_url":    fBare("адрес цен", 'm'),
			"balance_kind": fText("источник остатка", 'm').values(map[string]string{"api": "адрес провайдера", "estimate": "оценка по расходу", "none": "нет"}),
			"balance_low":  fNum("порог остатка", 'm', "без порога"),
		},
	},
	"modules.json": {
		Kind: NoteModules, Section: "Модули", Map: true, Noun: [3]string{"модуль", "модуля", "модулей"},
		Ref:     quoted,
		Subject: func(id string, e map[string]any) string { return "у модуля " + quoted(id, e) },
		AddOne:  "установлен %s", DelOne: "удалён %s", AddMany: "установлено %s", DelMany: "удалено %s",
		Fields: map[string]noteField{
			"name":           fIgnore,
			"title":          fText("название", 'n'),
			"about":          fBare("описание", 'n'),
			"needs":          fList("запрошенные права", 'p'),
			"pending_needs":  fIgnore, // запрошено при обновлении и ждёт решения: решение — в карточке
			"tools":          fIgnore, // выводится из кода
			"tasks":          fIgnore,
			"screens":        fIgnore,
			"secrets":        fBare("запрошенные секреты", 'p'), // имена секретов: по имени поля — без значений
			"granted":        fList("выданные секреты", 'p'),
			"memory":         fList("запрошенные области памяти", 'p'),
			"granted_memory": fList("выданные области памяти", 'p'),
			"ssh":            fList("запрошенные серверы", 'p'),
			"granted_hosts":  fList("выданные серверы", 'p'),
			"mcp":            fList("запрошенные серверы MCP", 'p'),
			"granted_mcp":    fList("выданные серверы MCP", 'p'),
			"skills":         fList("скиллы", 'p'),
			"on_handlers":    fMap("обработчики событий", 'p'),
			"granted_events": fList("выданные события", 'p'),
			"context":        fBare("функция контекста", 'f'),
			"blocks":         fMap("блоки на экранах", 'p'),
			"source":         fBare("источник кода", 'm'),
			"flag":           fBare("признак сбора", 'm'),
			"flag_title":     fBare("подпись признака", 'f'),
			"flag_default":   fBare("умолчание признака", 'n'),
			"code":           fBare("код", 'm'),
			"instructions":   fBare("инструкция", 'f'),
			"builtin":        fIgnore,
			"enabled":        fBool("включён", "включён", "выключен"),
			"broken":         fIgnore, // почему модуль выключился сам
			"breaks":         fIgnore, // чем уже ломался
			"added":          fIgnore,
		},
	},
	"mailconf.json": {
		Kind: NoteMail, Section: "Почта", Map: true, Noun: [3]string{"ящик", "ящика", "ящиков"},
		Ref:     func(id string, _ map[string]any) string { return "ящик " + id },
		Subject: func(id string, _ map[string]any) string { return "у ящика " + id },
		AddOne:  "%s — настройки заведены", DelOne: "%s — настройки убраны", AddMany: "заведено %s", DelMany: "убрано %s",
		Fields: map[string]noteField{
			"auth":              fText("тип входа", 'm'),
			"client_id":         fBare("идентификатор приложения", 'm'),
			"secret":            fSecret("ключ приложения"),
			"tenant":            fText("тенант", 'm'),
			"host":              fText("сервер", 'm'),
			"port":              fNum("порт", 'm', ""),
			"username":          fBare("логин", 'm'),
			"password":          fSecret("пароль"),
			"smtp_host":         fText("сервер SMTP", 'm'),
			"smtp_port":         fNum("порт SMTP", 'm', ""),
			"smtp_user":         fBare("логин SMTP", 'm'),
			"smtp_secret":       fSecret("пароль SMTP"),
			"smtp_starttls":     fBool("STARTTLS", "шифрует отправку (STARTTLS)", "отправляет без STARTTLS"),
			"archive":           fText("папка архива", 'f').with("{ref} — папка архива {new}", ""),
			"junk":              fText("папка спама", 'f').with("{ref} — папка спама {new}", ""),
			"skip":              fList("неразбираемые папки", 'p'),
			"folders":           fIgnore, // список папок с сервера, прочитан при входе
			"fetch_days":        fNum("глубина «Входящих»", 'f', "по умолчанию").with("{ref} — глубина «Входящих» {new}", ""),
			"junk_days":         fNum("срок спама", 'm', "по умолчанию").with("{ref} — срок спама {new}", ""),
			"keep_days":         fNum("срок хранения писем", 'm', "по умолчанию").with("{ref} — срок хранения писем {new}", ""),
			"purge_other":       fTri("стирание остальных писем", "стирает остальные письма", "не стирает остальные письма"),
			"purpose":           fText("назначение", 'n').with("{ref} — назначение: {new}", "").values(map[string]string{"personal": "личный", "work": "рабочий"}),
			"mode":              fText("режим", 'm').with("{ref} — режим: {new}", "").values(map[string]string{"tidy": "наводить порядок", "readonly": "только читать"}),
			"collect_receipts":  fIgnore, // старые поля, читаются ради прежних настроек
			"collect_attention": fIgnore,
			"collect":           fMap("что собирать", 'n'),
			"outbound":          fTri("доступ облачной модели", "открыт облачной модели", "закрыт облачной модели"),
			"connected":         fIgnore, // когда последний раз удался вход
			"last_err":          fIgnore,
			"Tasks":             fIgnore,
		},
	},
	"accounts.json": {
		Kind: NoteAccounts, Section: "Почта", IDField: "name", Noun: [3]string{"ящик", "ящика", "ящиков"},
		Ref:     func(id string, _ map[string]any) string { return "ящик " + id },
		Subject: func(id string, _ map[string]any) string { return "у ящика " + id },
		AddOne:  "заведён %s", DelOne: "убран %s", AddMany: "заведено %s", DelMany: "убрано %s",
		Fields: map[string]noteField{
			"name":     fIgnore,
			"email":    fBare("логин", 'm'),
			"password": fSecret("пароль"),
			"host":     fText("сервер", 'm'),
			"port":     fNum("порт", 'm', ""),
		},
	},
}

// NoteKeys — ключи составных настроек, для которых журнал умеет писать «что
// изменилось».
func NoteKeys() []string {
	var out []string
	for k := range noteSpecs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NoteKnows — описано ли поле составной настройки. Тест структуры, которую эта
// настройка хранит, сверяет по нему каждое её поле.
func NoteKnows(key, field string) bool {
	sp := noteSpecs[key]
	if sp == nil {
		return false
	}
	_, ok := sp.Fields[field]
	return ok
}

// NoteFallback — запись для составной настройки, у которой в журнале нет
// описания правки: она легла до того, как дверь стала его считать (значения в
// журнале режутся, из обрезка ничего не восстановить), или не разобралась.
func NoteFallback(key string) (ChangeNote, bool) {
	sp := noteSpecs[key]
	if sp == nil {
		return ChangeNote{}, false
	}
	return ChangeNote{Title: sp.Section + ": изменён список", Kind: sp.Kind}, true
}

// ---- разбор и сравнение ----

// parseEntities — сущности настройки по именам. Пустое значение — пустой набор;
// не JSON или не та форма — ok == false.
func parseEntities(sp *noteSpec, raw string) (map[string]map[string]any, bool) {
	out := map[string]map[string]any{}
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" {
		return out, true
	}
	if sp.Map {
		var m map[string]map[string]any
		if json.Unmarshal([]byte(raw), &m) != nil {
			return nil, false
		}
		for k, v := range m {
			if v == nil {
				v = map[string]any{}
			}
			out[k] = v
		}
		return out, true
	}
	var list []map[string]any
	if json.Unmarshal([]byte(raw), &list) != nil {
		return nil, false
	}
	for i, e := range list {
		id, _ := e[sp.IDField].(string)
		if id == "" {
			id, _ = e["email"].(string)
		}
		if id == "" {
			id = "№" + strconv.Itoa(i+1)
		}
		out[id] = e
	}
	return out, true
}

// fieldDiff — одно изменившееся поле.
type fieldDiff struct {
	Name     string
	F        noteField
	Old, New any
}

func emptyJSON(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

func sameValue(f noteField, a, b any) bool {
	if f.Tri {
		if a == nil && b == nil {
			return true
		}
	} else if emptyJSON(a) && emptyJSON(b) {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// sameSecret — секрет не изменился: сравниваются расшифрованные значения.
// Зашифрованное каждый раз другое, и по виду «изменён» значило бы «сохранён».
// Расшифровать нечем — не выдумываем изменение.
func (s *Store) sameSecret(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	pa, errA := s.box.Open(a)
	pb, errB := s.box.Open(b)
	if errA != nil || errB != nil {
		return true
	}
	return pa == pb
}

func (s *Store) diffEntity(sp *noteSpec, old, cur map[string]any) []fieldDiff {
	names := map[string]bool{}
	for k := range old {
		names[k] = true
	}
	for k := range cur {
		names[k] = true
	}
	var order []string
	for k := range names {
		order = append(order, k)
	}
	sort.Strings(order)
	var out []fieldDiff
	for _, name := range order {
		if name == sp.IDField {
			continue
		}
		f, known := sp.Fields[name]
		if !known {
			// поле, которого в описании нет: меняется — говорим «поле», значения нет
			f = noteField{Label: "поле «" + name + "»", Kind: noteBare, Gender: 'n'}
		}
		if f.Kind == noteIgnore {
			continue
		}
		var same bool
		if f.Kind == noteSecret {
			a, _ := old[name].(string)
			b, _ := cur[name].(string)
			same = s.sameSecret(a, b)
		} else {
			same = sameValue(f, old[name], cur[name])
		}
		if !same {
			out = append(out, fieldDiff{Name: name, F: f, Old: old[name], New: cur[name]})
		}
	}
	return out
}

// ---- слова ----

func pluralWord(n int, w [3]string) string {
	if n < 0 {
		n = -n
	}
	n %= 100
	if n > 10 && n < 20 {
		return w[2]
	}
	switch n % 10 {
	case 1:
		return w[0]
	case 2, 3, 4:
		return w[1]
	}
	return w[2]
}

func countNoun(n int, noun [3]string) string { return strconv.Itoa(n) + " " + pluralWord(n, noun) }

func changedWord(g byte) string {
	switch g {
	case 'f':
		return "изменена"
	case 'p':
		return "изменены"
	case 'n':
		return "изменено"
	}
	return "изменён"
}

// shorten — режет по рунам, а не по байтам: русский текст по байтам обрезается
// вдвое раньше и посреди буквы.
func shorten(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

func nameList(refs []string) string {
	if len(refs) <= noteNames {
		return strings.Join(refs, ", ")
	}
	return strings.Join(refs[:noteNames], ", ") + " и ещё " + strconv.Itoa(len(refs)-noteNames)
}

func flagWord(on bool) string {
	if on {
		return "да"
	}
	return "нет"
}

// show — значение поля человеку: коротко, без JSON.
func (f noteField) show(v any) string {
	switch f.Kind {
	case noteBool:
		b, ok := v.(bool)
		if !ok {
			return "по умолчанию"
		}
		return flagWord(b)
	case noteNum:
		n, ok := v.(float64)
		if !ok || n == 0 {
			if f.Zero != "" {
				return f.Zero
			}
			if !ok {
				return "не задано"
			}
		}
		return strconv.FormatFloat(n, 'f', -1, 64)
	case noteList:
		list, _ := v.([]any)
		var items []string
		for _, it := range list {
			items = append(items, fmt.Sprint(it))
		}
		if len(items) == 0 {
			return "пусто"
		}
		return shorten(nameList(items), noteValueMax)
	case noteMap:
		m, _ := v.(map[string]any)
		var pairs []string
		for k, val := range m {
			switch x := val.(type) {
			case nil:
				pairs = append(pairs, k+": по умолчанию")
			case bool:
				pairs = append(pairs, k+": "+flagWord(x))
			default:
				pairs = append(pairs, k+": "+fmt.Sprint(x))
			}
		}
		sort.Strings(pairs)
		if len(pairs) == 0 {
			return "пусто"
		}
		return shorten(nameList(pairs), noteValueMax)
	}
	text, _ := v.(string)
	if text == "" {
		if v != nil && !emptyJSON(v) {
			text = fmt.Sprint(v)
		} else {
			return "не задано"
		}
	}
	if word, ok := f.Values[text]; ok {
		text = word
	}
	return shorten(text, noteValueMax)
}

// hidesValue — значение поля в журнал не идёт.
func (f noteField) hidesValue() bool { return f.Kind == noteBare || f.Kind == noteSecret }

// title1 — название правки одного поля у одной сущности.
func (sp *noteSpec) title1(id string, e map[string]any, d fieldDiff) string {
	f := d.F
	ref := sp.Ref(id, e)
	if f.Kind == noteBool {
		b, ok := d.New.(bool)
		switch {
		case !ok:
			return ref + ": " + f.Label + " — по умолчанию"
		case b:
			return ref + " " + f.On
		}
		return ref + " " + f.Off
	}
	tpl := f.Tpl
	if f.TplNew != "" && emptyJSON(d.Old) {
		tpl = f.TplNew
	}
	if tpl == "" || f.hidesValue() {
		return sp.Subject(id, e) + " " + changedWord(f.Gender) + " " + f.Label
	}
	return strings.NewReplacer("{ref}", ref, "{old}", f.show(d.Old), "{new}", f.show(d.New), "{label}", f.Label).Replace(tpl)
}

// describeChange — что изменилось в составной настройке. ok == false — настройка не
// составная; service — изменилось только служебное.
func (s *Store) describeChange(key, before, after string, removed bool) (note ChangeNote, service, ok bool) {
	sp := noteSpecs[key]
	if sp == nil {
		return ChangeNote{}, false, false
	}
	note.Kind = sp.Kind
	if removed {
		note.Title = sp.Section + ": настройка со всем списком удалена"
		return note, false, true
	}
	old, okOld := parseEntities(sp, before)
	cur, okCur := parseEntities(sp, after)
	if !okOld || !okCur {
		note.Title = sp.Section + ": изменён список"
		return note, false, true
	}
	var added, gone, touched []string
	diffs := map[string][]fieldDiff{}
	for id := range cur {
		if _, had := old[id]; !had {
			added = append(added, id)
		}
	}
	for id := range old {
		if _, has := cur[id]; !has {
			gone = append(gone, id)
		}
	}
	for id, e := range cur {
		if o, had := old[id]; had {
			if d := s.diffEntity(sp, o, e); len(d) > 0 {
				touched = append(touched, id)
				diffs[id] = d
			}
		}
	}
	sort.Strings(added)
	sort.Strings(gone)
	sort.Strings(touched)
	n := len(added) + len(gone) + len(touched)
	refs := func(ids []string, from map[string]map[string]any) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, sp.Ref(id, from[id]))
		}
		return out
	}
	one := func(id string, goneNow bool) { note.Ref, note.Gone, note.Count = id, goneNow, 1 }
	title := ""
	switch {
	case n == 0:
		note.Title = sp.Section + ": служебное обновление"
		return note, true, true
	case len(touched) == 0 && len(gone) == 0 && len(added) == 1:
		one(added[0], false)
		title = fmt.Sprintf(sp.AddOne, sp.Ref(added[0], cur[added[0]]))
	case len(touched) == 0 && len(added) == 0 && len(gone) == 1:
		one(gone[0], true)
		title = fmt.Sprintf(sp.DelOne, sp.Ref(gone[0], old[gone[0]]))
	case len(touched) == 0 && len(gone) == 0:
		note.Count = n
		title = fmt.Sprintf(sp.AddMany, countNoun(n, sp.Noun)) + ": " + nameList(refs(added, cur))
	case len(touched) == 0 && len(added) == 0:
		note.Count = n
		title = fmt.Sprintf(sp.DelMany, countNoun(n, sp.Noun)) + ": " + nameList(refs(gone, old))
	case n == 1: // одна сущность, изменилось у неё
		id := touched[0]
		one(id, false)
		ds := diffs[id]
		if len(ds) == 1 {
			title = sp.title1(id, cur[id], ds[0])
			if !ds[0].F.hidesValue() && ds[0].F.Kind != noteBool {
				note.Was, note.Now = ds[0].F.show(ds[0].Old), ds[0].F.show(ds[0].New)
			}
		} else {
			var labels []string
			for _, d := range ds {
				labels = append(labels, d.F.Label)
			}
			title = sp.Subject(id, cur[id]) + " изменено: " + nameList(labels)
		}
	default:
		note.Count = n
		title = sp.sameFieldTitle(added, gone, touched, diffs, cur, countNoun(n, sp.Noun))
		if title == "" {
			title = "изменён список (" + countNoun(n, sp.Noun) + ")"
		}
		if len(added) == 0 && len(gone) == 0 {
			title += ": " + nameList(refs(touched, cur))
		}
	}
	note.Title = shorten(sp.Section+": "+title, noteTitleMax)
	return note, false, true
}

// sameFieldTitle — у всех затронутых сущностей изменилось одно и то же поле
// («Принять все» цены): тогда так и сказано, вместо «изменён список».
func (sp *noteSpec) sameFieldTitle(added, gone, touched []string, diffs map[string][]fieldDiff, cur map[string]map[string]any, count string) string {
	if len(added) > 0 || len(gone) > 0 || len(touched) < 2 {
		return ""
	}
	first := diffs[touched[0]]
	if len(first) != 1 {
		return ""
	}
	for _, id := range touched {
		if d := diffs[id]; len(d) != 1 || d[0].Name != first[0].Name {
			return ""
		}
	}
	return "изменено «" + first[0].F.Label + "» — " + count
}
