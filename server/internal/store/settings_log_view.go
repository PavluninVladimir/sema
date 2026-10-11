package store

// Журнал настроек глазами человека: «ваши правки» отдельно от служебного.
//
// Журнал пишет всё, что меняется в таблице настроек, и пишет без разбора:
// курсоры почтовых папок, «прочитано до» у шести тысяч чатов, время последней
// проверки, последняя ошибка канала. Человек за этим не стоит, но в одном
// списке с его правками такие отметки топили настоящее: на Обзоре «последние
// пять изменений» состояли из `chat.read.71` (запись 1 октября 2026).
//
// Решение владельца по этому вопросу не получено (стоял вопрос: перестать
// ли писать отметки вовсе или писать и прятать), поэтому выбран вариант без
// потери данных: **отметки по-прежнему пишутся и хранятся ровно так, как
// раньше**, а прячутся только на показе. По умолчанию журнал — «Ваши правки»,
// чип «Служебные» показывает остальное, чип «Все» — всё как есть.
//
// Что считать служебной отметкой — не список в коде, а настройка
// `settings_log.marks` («Тонкая настройка» журнала) с первичным заполнением.
// Отметка узнаётся по ключу, а не по тому, кто её записал: фоновая запись,
// попавшая в долю секунды человеческого действия, подписывается его именем
// (`SignChanges`), и без отбора по ключу курсор выглядел бы правкой человека.

import (
	"encoding/json"
	"strings"
)

// SettingsLogMarks — настройка со списком служебных отметок журнала.
//
// Формат: через запятую или с новой строки. «chat.read.» — всё, что начинается
// так; «*.last_err» — всё, что так заканчивается; «*.cursor.*» — всё, что так
// содержит. Пустая строка — служебных отметок нет, и в «Ваши правки» попадает
// всё, что подписано человеком.
var SettingsLogMarks = DefText("settings_log.marks", settingsLogMarksSeed)

// settingsLogMarksSeed — первичное заполнение: что Сёма пишет сам и по
// десять раз в минуту. Ключи сверены с тем, что реально лежит в таблице
// настроек: курсоры и отметки чатов, истории и лент Telegram, счётчики,
// остановки ролей, «когда в прошлый раз», последние ошибки.
//
// Заполнение растёт версиями (грабли «Справочник в базе не обновляется сам»):
// настройка на живой базе уже лежит строкой, и новые образцы сами туда не попадут.
// `seedLogMarks` при запуске досыпает то, чего в засеянном прошлый раз не было, не
// трогая добавленное и убранное человеком. Версия 1 — то, что выехало 2 октября 2026
// первой выкаткой; версия 2 — образцы, найденные по живому журналу того же дня.
const settingsLogMarksSeed = settingsLogMarksSeedV1 + ", " + settingsLogMarksAddedV2

// settingsLogMarksSeedV1 — первая выкладка заполнения. Нужна миграции как точка
// отсчёта: у базы, где не помнят, что засевали, считается, что засеяно это.
const settingsLogMarksSeedV1 = "chat.read., chat.done., chat.expires., chat.notify., chat.msg., chat.msgref., msg.chat., notify.chat., " +
	"memory.chat_session., folders.cursor., tg.history., tg.feed., tg.code_hash., tg.draft_off., tg.posted., tg.nodiscussion., " +
	"tg.wait_skip., max.posted., role.cooldown., watch.count., night.last., balance.state., price.last., price.offered., " +
	"price.proposal., digest.seen, digest.since, memory.last_pass, memory.review_last, backup.last, images.checked, " +
	"*.last_err, *_last_err"

// settingsLogMarksAddedV2 — что добавила версия 2. Решения по картинкам и привязки
// чатов подписаны человеком (машина пишет их в долю секунды его действия), но правкой
// настройки не являются: это отметки Сёмы. Остальное — счётчики, кэши и «когда в
// прошлый раз», найденные обходом исходников (`TestEveryKeyPrefixHasAnOwner`).
// Отдельно — одиночные ключи, которые машина пишет внутри действия человека
// (`archive_folder` — имя архива с прошлого прохода, `models_removed` — память об
// удалённых моделях, `max.captcha_link` и `max.verify_token` — промежуточные шаги
// входа в MAX, `health.period` — выбранное окно на экране, а не настройка).
const settingsLogMarksAddedV2 = "vision.decision., vision_state, max.history., max.chat., tg.chat., tg.quiet., tg.refs., " +
	"chat.nudges., role.fails., watch.doctor.last., watch.disagree.last, ssh.recard., providers.seen., providers.seed, script., " +
	"images.cheapest, images.unfit, memory.suggestions.json, queue.cloud_note, " +
	"archive_folder, models_removed, mail.translate_langs, max.captcha_link, max.verify_token, health.period"

// settingsLogMarksSeededKey — что засевали в прошлый раз: список образцов. Нужен,
// чтобы отличить образец, который человек убрал (он в засеянном, но не в настройке:
// не возвращаем), от образца, которого ещё не было (он не в засеянном: добавляем).
const settingsLogMarksSeededKey = "settings_log.marks_seeded"

// seedLogMarks — досыпать новые образцы служебных отметок в настройку, которая уже
// сохранена на живой базе. Правила: настройки нет (читается умолчание) — досыпать
// нечего, умолчание и так новое; настройка есть — к ней добавляются образцы
// нынешнего заполнения, которых не было в засеянном прошлый раз и нет в самой
// настройке; убранное человеком не возвращается, добавленное остаётся, а пустой
// сохранённый список («ничего служебным не считать») остаётся пустым. Что засеяно,
// запоминается (`settingsLogMarksSeededKey`); базе без этой отметки точкой отсчёта
// служит первая выкладка (V1).
func (s *Store) seedLogMarks() {
	current := ParseSettingMarks(settingsLogMarksSeed)
	was := ParseSettingMarks(settingsLogMarksSeedV1)
	if raw, ok := s.settingRow(settingsLogMarksSeededKey); ok {
		was = ParseSettingMarks(raw)
		if sameList(was, current) {
			return
		}
	}
	// пустой список, сохранённый человеком, — осознанный выбор «ничего служебным не
	// считать»: возвращать ему отметки значило бы переписать его решение
	if raw, ok := s.settingRow(SettingsLogMarks.Key); ok && len(ParseSettingMarks(raw)) > 0 {
		have := ParseSettingMarks(raw)
		changed := false
		for _, mark := range current {
			if !containsMark(was, mark) && !containsMark(have, mark) {
				have = append(have, mark)
				changed = true
			}
		}
		if changed {
			if err := s.SetSetting(SettingsLogMarks.Key, strings.Join(have, ", ")); err != nil {
				return // не записалось — и «засеяно» не пишем: попробуем при следующем запуске
			}
		}
	}
	_ = s.SetSetting(settingsLogMarksSeededKey, strings.Join(current, ", "))
}

// settingRow — строка настройки как она лежит, и есть ли она вообще: «пусто» и
// «не задана» здесь разные ответы (пустой список отметок — осознанный выбор).
func (s *Store) settingRow(key string) (string, bool) {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

func containsMark(list []string, mark string) bool {
	for _, m := range list {
		if m == mark {
			return true
		}
	}
	return false
}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Отбор журнала настроек: слова — договор экрана и хранилища, как у
// `SSHLogFiltered`.
const (
	// SettingsLogMine — «Ваши правки»: подписано человеком и не служебная отметка
	SettingsLogMine = ""
	// SettingsLogService — «Служебные»: всё, что человеку правкой не считается —
	// фоновые записи Сёмы и отметки из списка `settings_log.marks`
	SettingsLogService = "служебные"
	// SettingsLogAll — «Все»: журнал целиком, как он лежит
	SettingsLogAll = "все"
)

// NameSmellsSecret — имя параметра говорит о секрете («tg.api_hash»,
// «google.refresh_token»). Тот же признак, которым журнал прячет значения:
// индекс настроек им же решает, что значение показывать нельзя.
func NameSmellsSecret(key string) bool { return nameSmellsSecret(key) }

// ParseSettingMarks — список служебных отметок из строки настройки.
func ParseSettingMarks(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == ';' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// SettingMarks — служебные отметки, как они заданы сейчас.
func (s *Store) SettingMarks() []string {
	return ParseSettingMarks(s.SettingText(SettingsLogMarks))
}

// IsServiceMark — ключ служебный: подходит под одну из отметок.
func IsServiceMark(key string, marks []string) bool {
	for _, m := range marks {
		if matchMark(key, m) {
			return true
		}
	}
	return false
}

func matchMark(key, mark string) bool {
	switch {
	case strings.HasPrefix(mark, "*") && strings.HasSuffix(mark, "*") && len(mark) > 2:
		return strings.Contains(key, mark[1:len(mark)-1])
	case strings.HasPrefix(mark, "*"):
		return strings.HasSuffix(key, mark[1:])
	case strings.HasSuffix(mark, "*"):
		return strings.HasPrefix(key, mark[:len(mark)-1])
	}
	return strings.HasPrefix(key, mark)
}

// markClause — условие SQL «ключ — служебная отметка» и его параметры. Отметок
// нет — условие всегда ложно.
func markClause(marks []string) (string, []any) {
	if len(marks) == 0 {
		return "0", nil
	}
	var parts []string
	var args []any
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	for _, m := range marks {
		switch {
		case strings.HasPrefix(m, "*") && strings.HasSuffix(m, "*") && len(m) > 2:
			args = append(args, "%"+esc.Replace(m[1:len(m)-1])+"%")
		case strings.HasPrefix(m, "*"):
			args = append(args, "%"+esc.Replace(m[1:]))
		case strings.HasSuffix(m, "*"):
			args = append(args, esc.Replace(m[:len(m)-1])+"%")
		default:
			args = append(args, esc.Replace(m)+"%")
		}
		parts = append(parts, `key LIKE ? ESCAPE '\'`)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// logWhere — условие отбора и его параметры. Новые записи условием не
// трогаем: «Все» не отсеивает ничего.
func (s *Store) logWhere(filter string) (string, []any) {
	marks, args := markClause(s.SettingMarks())
	switch filter {
	case SettingsLogMine, "люди":
		return ` WHERE who <> '' AND svc = 0 AND NOT ` + marks, args
	case SettingsLogService, "сам":
		return ` WHERE (who = '' OR svc = 1 OR ` + marks + `)`, args
	}
	return "", nil
}

// SettingsLogView — журнал глазами человека, новые сверху. Отбор — слова
// `SettingsLogMine` / `SettingsLogService` / `SettingsLogAll`; прежние «люди» и
// «сам» (закладки, ссылки) читаются как «ваши» и «служебные».
func (s *Store) SettingsLogView(filter string, limit int) ([]SettingChange, error) {
	where, args := s.logWhere(filter)
	return s.settingsLogQuery(where, args, limit)
}

// SettingsLogViewSince — то же с нижней границей по времени (RFC3339).
func (s *Store) SettingsLogViewSince(filter, since string, limit int) ([]SettingChange, error) {
	where, args := s.logWhere(filter)
	if where == "" {
		where = ` WHERE at >= ?`
	} else {
		where += ` AND at >= ?`
	}
	return s.settingsLogQuery(where, append(args, since), limit)
}

func (s *Store) settingsLogQuery(where string, args []any, limit int) ([]SettingChange, error) {
	rows, err := s.db.Query(`SELECT id, at, key, old_val, new_val, who, source, hidden, removed, note, svc
		FROM settings_log`+where+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SettingChange
	for rows.Next() {
		var chg SettingChange
		var hidden, removed, svc int
		var note string
		if err := rows.Scan(&chg.ID, &chg.At, &chg.Key, &chg.Before, &chg.After,
			&chg.Who, &chg.From, &hidden, &removed, &note, &svc); err != nil {
			return nil, err
		}
		chg.Hidden, chg.Removed, chg.Service = hidden == 1, removed == 1, svc == 1
		if note != "" {
			var n ChangeNote
			if json.Unmarshal([]byte(note), &n) == nil && n.Title != "" {
				chg.Note = &n
			}
		}
		if chg.Note == nil {
			// легло до описания правок или не разобралось: из обрезанных значений
			// ничего не восстановить, остаётся обобщённая строка
			if n, ok := NoteFallback(chg.Key); ok {
				chg.Note = &n
			}
		}
		out = append(out, chg)
	}
	return out, rows.Err()
}

// CountSettingsLogView — сколько записей в каждом отборе: ваши, служебные, всего.
func (s *Store) CountSettingsLogView() (mine, service, total int) {
	where, args := s.logWhere(SettingsLogMine)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM settings_log`+where, args...).Scan(&mine)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM settings_log`).Scan(&total)
	return mine, total - mine, total
}

// ChangedKeysSince — какие параметры менялись после метки (`LastChange`):
// без повторов. Действие, после которого настройки не менялись вовсе, возвращает
// пустой список.
func (s *Store) ChangedKeysSince(after int64) []string {
	rows, err := s.db.Query(`SELECT DISTINCT key FROM settings_log WHERE id > ?`, after)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out = append(out, k)
		}
	}
	return out
}

// KeyEdits — сколько раз человек правил один параметр за срок и чем это
// кончилось в последний раз.
type KeyEdits struct {
	Key    string
	Count  int
	LastID int64
	// Source — экран и действие последней правки: по ним видно, где параметр
	// живёт, когда по ключу этого не узнать
	Source string
}

// TopEditedKeys — параметры, которые человек менял чаще всего с момента `since`
// (RFC3339): служебные отметки не считаются, при равенстве выше тот, что
// правили позже. Берёт с запасом `limit`: часть параметров на экран не попадёт.
//
// Отметки отсеиваются здесь, а не условием SQL: шесть десятков LIKE SQLite
// проверял на каждой из восьми тысяч строк журнала, и «Под рукой» стоил на
// Pi 55–70 мс — самый долгий запрос корня «Настроек» (журнал медленных
// запросов, 5 октября 2026). Людских ключей за месяц — десятки.
func (s *Store) TopEditedKeys(since string, limit int) ([]KeyEdits, error) {
	marks := s.SettingMarks()
	rows, err := s.db.Query(`SELECT key, COUNT(*) AS n, MAX(id) AS last,
			COALESCE((SELECT source FROM settings_log l2 WHERE l2.key = l.key AND l2.who <> '' ORDER BY l2.id DESC LIMIT 1), '')
		FROM settings_log l
		WHERE who <> '' AND svc = 0 AND at >= ? AND removed = 0 AND hidden = 0
		GROUP BY key ORDER BY n DESC, last DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeyEdits
	for rows.Next() {
		var k KeyEdits
		if err := rows.Scan(&k.Key, &k.Count, &k.LastID, &k.Source); err != nil {
			return nil, err
		}
		if IsServiceMark(k.Key, marks) {
			continue
		}
		out = append(out, k)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, rows.Err()
}
