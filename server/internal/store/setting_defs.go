package store

// Умолчания настроек — в одном месте.
//
// Пока предел читался как `e.settingInt("mcp.error_chars", 1500)`, число
// 1500 жило в каждом месте, где его читали (у сути ошибки их два), а
// экран, который показывает «по умолчанию 1500», писал его в подсказке
// своей рукой. Любое из мест можно было поправить, не тронув остальные, и
// подпись «всё по умолчанию» на экране тогда врала бы, не споря с кодом.
//
// Теперь у настройки одно описание — `SettingDef`: ключ, вид, умолчание и
// границы. Описание кладёт в реестр тот пакет, чья это настройка
// (`DefPositive` и родня на уровне пакета), читает настройку через него же
// (`Store.SettingInt` и родня), а экран спрашивает у реестра по ключу
// (`LookupSetting`): что лежит сейчас, что было бы по умолчанию и что
// допустимо вписать. Второго списка чисел нет ни у кого.
//
// Вид решает, как читается значение, и это не мелочь. У «положительного»
// ноль и мусор в таблице читаются как «не задана» (умолчание), у «счёта»
// ноль законен: «0 — не надо». Прежние читатели различались ровно так
// (`settingInt` и `settingCount`), и поведение осталось тем же.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Виды настройки.
const (
	// SettingPositive — целое больше нуля; ноль, мусор и пустое — «не задана»
	SettingPositive = "positive"
	// SettingCount — целое от нуля: 0 законен («не надо», «никогда»)
	SettingCount = "count"
	// SettingFlag — включено или нет: «1» / «0»
	SettingFlag = "flag"
	// SettingText — строка как есть; умолчание часто пустое («не задан»)
	SettingText = "text"
)

// SettingDef — описание одной настройки. Значение лежит в таблице
// `settings` под Key строкой; умолчание — то, что прочтётся, пока строки нет.
type SettingDef struct {
	Key     string
	Kind    string
	Default string
	// Min и Max — что допустимо вписать с экрана. Max == 0 — без верхней
	// границы: прежние действия сохранения проверяли только низ, и
	// описание их не сужает
	Min, Max int
	// Careful — настройку нельзя менять «с ходу»: вход и доступ, доверие,
	// самостоятельность серверов, то, что открывает порт или выпускает данные
	// наружу. Признак живёт у настройки, а не списком в коде того, кто её
	// выставляет на виду: блок «Под рукой» на Обзоре такие настройки не берёт
	// никогда, даже если человек менял их чаще всех, — править их надо на своём
	// экране, где рядом стоит объяснение
	Careful bool
}

var (
	settingDefsMu sync.RWMutex
	settingDefs   = map[string]SettingDef{}
)

// registerSetting — положить описание в реестр. Ключ заводится один раз:
// второе описание того же ключа — это и есть второй список чисел, ради
// избавления от которого реестр заведён, поэтому здесь паника при запуске, а
// не тихая замена.
func registerSetting(d SettingDef) SettingDef {
	settingDefsMu.Lock()
	defer settingDefsMu.Unlock()
	if _, dup := settingDefs[d.Key]; dup {
		panic("настройка " + d.Key + " описана дважды: умолчание у неё одно")
	}
	settingDefs[d.Key] = d
	return d
}

// Careful — пометить уже описанную настройку осторожной (см. SettingDef.Careful).
// Оборачивает описание: `store.Careful(store.DefFlag(key, false))`; настройка
// остаётся одной записью реестра, а не заводится вторым списком.
func Careful(d SettingDef) SettingDef {
	settingDefsMu.Lock()
	defer settingDefsMu.Unlock()
	d.Careful = true
	settingDefs[d.Key] = d
	return d
}

// DefPositive — целая настройка больше нуля (`settingInt` прежних читателей).
func DefPositive(key string, def, min, max int) SettingDef {
	return registerSetting(SettingDef{Key: key, Kind: SettingPositive, Default: strconv.Itoa(def), Min: min, Max: max})
}

// DefCount — целая настройка от нуля (`settingCount` прежних читателей).
func DefCount(key string, def, min, max int) SettingDef {
	return registerSetting(SettingDef{Key: key, Kind: SettingCount, Default: strconv.Itoa(def), Min: min, Max: max})
}

// DefFlag — включено или выключено.
func DefFlag(key string, def bool) SettingDef {
	return registerSetting(SettingDef{Key: key, Kind: SettingFlag, Default: flagValue(def)})
}

// DefText — строка. Пустое умолчание значит «не задана».
func DefText(key, def string) SettingDef {
	return registerSetting(SettingDef{Key: key, Kind: SettingText, Default: def})
}

func flagValue(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

// LookupSetting — описание по ключу. Экран строит строку настройки из него,
// а не из чисел, вписанных рядом.
func LookupSetting(key string) (SettingDef, bool) {
	settingDefsMu.RLock()
	defer settingDefsMu.RUnlock()
	d, ok := settingDefs[key]
	return d, ok
}

// SettingDefs — все описания по ключу: для тестов, которые обязаны
// перебрать их, а не проверить одно.
func SettingDefs() []SettingDef {
	settingDefsMu.RLock()
	defer settingDefsMu.RUnlock()
	out := make([]SettingDef, 0, len(settingDefs))
	for _, d := range settingDefs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// DefaultInt — умолчание числом (для не числовых — 0).
func (d SettingDef) DefaultInt() int {
	n, _ := strconv.Atoi(d.Default)
	return n
}

// DefaultFlag — умолчание «включено».
func (d SettingDef) DefaultFlag() bool { return d.Default == "1" }

// ---- чтение ----

// SettingInt — число настройки по её виду: у положительной ноль и мусор дают
// умолчание, у счёта ноль остаётся нулём.
func (s *Store) SettingInt(d SettingDef) int {
	// Sscanf, а не Atoi: прежние читатели разбирали так, и «12 штук» у них
	// давало 12 — поведение не должно сместиться вместе с местом умолчания
	var v int
	_, err := fmt.Sscanf(s.Setting(d.Key, ""), "%d", &v)
	switch d.Kind {
	case SettingCount:
		if err == nil && v >= 0 {
			return v
		}
	default:
		if err == nil && v > 0 {
			return v
		}
	}
	return d.DefaultInt()
}

// SettingFlag — включена ли настройка. Читаются те же записи, что писали
// прежние читатели: «1» и «true» — да, «0» и «false» — нет, остальное —
// умолчание.
func (s *Store) SettingFlag(d SettingDef) bool {
	switch strings.ToLower(strings.TrimSpace(s.Setting(d.Key, ""))) {
	case "1", "true", "on", "да":
		return true
	case "0", "false", "off", "нет":
		return false
	}
	return d.DefaultFlag()
}

// SettingText — строка настройки; нет строки — умолчание.
func (s *Store) SettingText(d SettingDef) string {
	return s.Setting(d.Key, d.Default)
}

// SettingShown — то, что настройка значит сейчас, строкой, как её видит
// поле на экране: число, «true» / «false» у флага, текст. Читается тем же
// путём, что и движком, и потому не расходится с ним.
func (s *Store) SettingShown(d SettingDef) string {
	switch d.Kind {
	case SettingPositive, SettingCount:
		return strconv.Itoa(s.SettingInt(d))
	case SettingFlag:
		if s.SettingFlag(d) {
			return "true"
		}
		return "false"
	}
	return s.SettingText(d)
}

// ShownDefault — умолчание в том же виде, в каком его показывает SettingShown.
func (d SettingDef) ShownDefault() string {
	if d.Kind == SettingFlag {
		if d.DefaultFlag() {
			return "true"
		}
		return "false"
	}
	return d.Default
}

// ---- запись ----

// Check — проверить значение, пришедшее с экрана, и вернуть его в том виде,
// в каком оно ляжет в таблицу. Ошибка говорит человеку, какое число нужно.
func (d SettingDef) Check(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch d.Kind {
	case SettingPositive, SettingCount:
		v, err := strconv.Atoi(raw)
		min := d.Min
		if d.Kind == SettingPositive && min < 1 {
			min = 1
		}
		if d.Kind == SettingCount && min < 0 {
			min = 0
		}
		if err != nil || v < min || (d.Max > 0 && v > d.Max) {
			if d.Max > 0 {
				return "", fmt.Errorf("нужно целое число от %d до %d", min, d.Max)
			}
			return "", fmt.Errorf("нужно целое число от %d", min)
		}
		return strconv.Itoa(v), nil
	case SettingFlag:
		switch strings.ToLower(raw) {
		case "1", "true", "on", "да":
			return "1", nil
		case "0", "false", "off", "нет":
			return "0", nil
		}
		return "", fmt.Errorf("нужно «включено» или «выключено»")
	}
	return raw, nil
}

// SaveSetting — записать настройку, проверив значение по её описанию.
func (s *Store) SaveSetting(d SettingDef, raw string) error {
	value, err := d.Check(raw)
	if err != nil {
		return err
	}
	return s.SetSetting(d.Key, value)
}
