package store

// Журнал изменений настроек: кто, когда, какой параметр, старое и новое
// значение.
//
// До 11 сентября 2026 такой истории не было вовсе. На «Главном» стоял
// блок «Последняя активность», собранный из журналов доступа, секретов и
// решений по скиллам, и под ним честно было написано: истории правок
// здесь нет и обещать её нельзя. Обещать нельзя было потому, что настройка
// менялась в двух сотнях мест кода, и ни одно из них ничего не записывало.
//
// Дверь здесь одна и держится построением, а не обещанием: таблицу
// `settings` во всём дереве пишет ровно один `Store.SetSetting` (плюс
// `DeleteSettingKey`), и запись в журнал стоит внутри него. Новый экран
// настроек, новый воркер, новый модуль — любой путь к настройке проходит
// через ту же функцию и попадает в журнал сам, не зная о нём. Это не
// вежливая просьба к будущему автору, а единственный писатель таблицы:
// `TestНастройкиПишетТолькоДверь` перебирает исходники и роняет сборку,
// если SQL к `settings` появится где-то ещё.
//
// Мы уже наступали на противоположное: «единственная дверь»
// `mayGoOutside` год стояла в коде, её не звал ни один рабочий путь, а
// тест засчитывал за проверку комментарий рядом. Поэтому проверка здесь
// смотрит на построение (кто пишет таблицу), а не на намерения.
//
// Значения секретов в журнал не попадают никогда — см. `значениеДляЖурнала`.

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/journal"
	"github.com/PavluninVladimir/sema/server/internal/secret"
)

// SettingChange — одна строка журнала. Она же — то, что знает о правке
// сама дверь в момент записи.
type SettingChange struct {
	ID     int64
	At     string // RFC3339, как всё время в базе
	Key    string
	Before string
	After  string
	Who    string // «вы», «токен «codex»» — пусто значит «никто не заявил себя»
	From   string // экран и действие: «settings:notify · save_notify»
	// Скрыто — значение опознано как секрет и в журнал не попало. Флаг
	// нужен экрану: «не показано, потому что секрет» и «значение было
	// пустым» — разные вещи, и путать их нельзя.
	Hidden  bool
	Removed bool // параметр удалён целиком, а не изменён
	// Note — что изменилось в составной настройке (`models.json`…) словами:
	// считает сама дверь, пока видны оба значения целиком. nil — настройка не
	// составная; у записей, легших до описания, при чтении подставляется
	// обобщённая строка (`NoteFallback`)
	Note *ChangeNote
	// Service — в составной настройке изменилось только служебное (последний
	// результат роли, «подключён»): запись правкой человека не считается, даже
	// если подписана его действием
	Service bool
	// secret — вход двери: вызывающий сам знает, что кладёт секрет
	// (SetSecretSetting). Приметам это не оставляем: без ключа шифрования
	// `seal` возвращает значение открытым текстом, и по виду оно ничем не
	// отличается от обычной настройки.
	secret bool
}

// logValueMax — сколько знаков значения храним. Настройка бывает
// на сотни килобайт (список моделей, настройки ящиков); «старое → новое»
// такого размера всё равно никто не прочитает, а место журнал займёт.
// Режем по рунам: русский текст по байтам обрезается вдвое раньше.
const logValueMax = 300

// keepSettingsLogPerKey — сколько правок одного параметра держим. Курсоры
// папок и прочие метки, которые Сёма переписывает каждый проход, иначе
// вытеснили бы из журнала настоящие правки. Ограничение на ключ, а не на
// журнал целиком: редкая правка редкого параметра должна пережить
// болтливого соседа.
const keepSettingsLogPerKey = 200

func (s *Store) migrateSettingsLog() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS settings_log (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			at      TEXT NOT NULL,
			key     TEXT NOT NULL,
			old_val TEXT NOT NULL DEFAULT '',
			new_val TEXT NOT NULL DEFAULT '',
			who     TEXT NOT NULL DEFAULT '',
			source  TEXT NOT NULL DEFAULT '',
			hidden  INTEGER NOT NULL DEFAULT 0,
			removed INTEGER NOT NULL DEFAULT 0,
			note    TEXT NOT NULL DEFAULT '',
			svc     INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_settings_log_at ON settings_log(id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_settings_log_key ON settings_log(key, id DESC)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	// журнал, созданный до описания правок составных настроек, колонок не имеет
	for _, col := range []struct{ name, ddl string }{
		{"note", `ALTER TABLE settings_log ADD COLUMN note TEXT NOT NULL DEFAULT ''`},
		{"svc", `ALTER TABLE settings_log ADD COLUMN svc INTEGER NOT NULL DEFAULT 0`},
	} {
		if !s.hasColumn("settings_log", col.name) {
			if _, err := s.db.Exec(col.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

// logSettingChange — сама дверь. Зовётся из `SetSetting` и
// `DeleteSettingKey` и больше ниоткуда: писать журнал в обход того, кто
// меняет настройку, значило бы снова разойтись с правдой.
func (s *Store) logSettingChange(chg SettingChange) {
	if chg.At == "" {
		chg.At = time.Now().Format(time.RFC3339)
	}
	before, hidBefore := valueForLog(chg.Key, chg.Before, chg.secret)
	after, hidAfter := valueForLog(chg.Key, chg.After, chg.secret)
	hidden, removed := 0, 0
	if hidBefore || hidAfter {
		hidden = 1
	}
	if chg.Removed {
		removed = 1
	}
	// составная настройка: что изменилось, считается по значениям целиком — в
	// журнал они уходят уже обрезанными, и из обрезка JSON ничего не восстановить
	note, svc := "", 0
	if chg.Note == nil && !chg.secret {
		if n, service, ok := s.describeChange(chg.Key, chg.Before, chg.After, chg.Removed); ok {
			chg.Note, chg.Service = &n, service
		}
	}
	if chg.Note != nil {
		if raw, err := json.Marshal(chg.Note); err == nil {
			note = string(raw)
		}
	}
	if chg.Service {
		svc = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO settings_log (at, key, old_val, new_val, who, source, hidden, removed, note, svc)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chg.At, chg.Key, before, after, chg.Who, chg.From, hidden, removed, note, svc)
	if err != nil {
		return
	}
	_, _ = s.db.Exec(`DELETE FROM settings_log WHERE key = ? AND id NOT IN (
		SELECT id FROM settings_log WHERE key = ? ORDER BY id DESC LIMIT ?)`,
		chg.Key, chg.Key, keepSettingsLogPerKey)
}

// valueForLog — что из значения настройки можно написать в журнал.
// Возвращает текст и признак «значение скрыто».
//
// Секрет узнаётся тремя способами, и все три по виду, а не по списку
// известных имён — список всегда отстаёт от жизни:
//
//  1. вызывающий сам сказал, что кладёт секрет (`SetSecretSetting`);
//  2. значение зашифровано (`enc:v1:…`);
//  3. имя параметра говорит о секрете (`google.refresh_token`,
//     `tg.api_hash`). Это единственный признак, когда ключа шифрования
//     нет и значение лежит открытым текстом: сам токен тогда выглядит как
//     обычная строка и никакими приметами не ловится.
//
// Что не поймано ни одним из трёх — проходит через маски журнала
// (`journal.Замаскировать`): они ловят закрытый ключ, `Bearer`,
// `Authorization`, пароль в строке подключения и в JSON. Так из
// `models.json` уезжает ключ провайдера, хотя сама настройка секретной не
// объявлена.
//
// Ошибка в пользу молчания: параметр, попавший под примету зря, потеряет
// в журнале значение, но не смысл — ключ, время и «кто» остаются. Обратная
// ошибка стоит утечки, а журнал человек присылает в переписку.
func valueForLog(key, value string, isSecret bool) (string, bool) {
	if value == "" {
		return "", false
	}
	if isSecret || secret.Encrypted(value) || (nameSmellsSecret(key) && !isThreshold(value)) {
		return "", true
	}
	masked := journal.Mask(value, nil)
	return clipRunes(masked, logValueMax), masked != value
}

// secretWords — из чего состоит имя параметра, за которым стоит
// секрет. Сравнение посегментное («tg.api_hash» → tg, api, hash), а не по
// вхождению: иначе «api_id» и «keep_days» прятались бы вместе с «api_key».
// Слово «auth» сюда не входит нарочно: `auth.local_open` и `auth.rp_id` —
// обычные настройки входа, и прятать их значило бы прятать полнастроек.
var secretWords = map[string]bool{
	"secret": true, "secrets": true, "password": true, "passwd": true,
	"pass": true, "token": true, "tokens": true, "key": true, "keys": true,
	"apikey": true, "hash": true, "session": true, "cookie": true,
	"credentials": true,
}

func nameSmellsSecret(key string) bool {
	for _, seg := range strings.FieldsFunc(strings.ToLower(key), func(r rune) bool {
		return r == '.' || r == ':' || r == '_' || r == '-' || r == '/'
	}) {
		if secretWords[seg] {
			return true
		}
	}
	return false
}

// isThreshold — значение, которое секретом быть не может: число, срок,
// тумблер. Без этой оговорки `memory.session_idle_h = 5` пряталось бы
// из-за слова «session» в имени, и человек не видел бы, что сам же
// поменял порог. Токеном такое значение не бывает: ключи и пароли
// цифрами целиком не записывают.
func isThreshold(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "false", "on", "off", "yes", "no", "да", "нет":
		return true
	}
	for _, r := range value {
		if (r < '0' || r > '9') && r != '.' && r != ',' && r != '-' && r != '+' {
			return false
		}
	}
	return value != ""
}

// LastChange — номер последней строки журнала. Метка для
// `ПодписатьПравки`: что легло после неё, то сделано текущим действием.
func (s *Store) LastChange() int64 {
	var id int64
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM settings_log`).Scan(&id)
	return id
}

// SignChanges — назвать человека у правок, которые легли в журнал
// после метки и никем ещё не заявлены.
//
// Это вторая дверь, и она отвечает на другой вопрос: первая знает, ЧТО
// изменилось, эта — КТО. Стоит она в `api.Server.выполнить`, вокруг
// единственного пути действия человека, и потому не зависит от того,
// какой дорогой действие добралось до настройки: через `store` напрямую,
// через сеттер движка, через модуль. Подписывать по списку мест вызова мы
// пробовали мысленно и отказались — новый экран настроек просто забыл бы
// назваться, и журнал молча врал бы «Сёма сам».
//
// Цена: параметр, который сам Сёма поменял фоном в те же миллисекунды,
// будет подписан человеком. Фоновых записей единицы в минуту, действие
// живёт десятки миллисекунд, и ключ в строке всё равно настоящий — врать
// про параметр журнал не начнёт, только про руку.
func (s *Store) SignChanges(after int64, who, from string) {
	if who == "" {
		return
	}
	_, _ = s.db.Exec(`UPDATE settings_log SET who = ?, source = ? WHERE id > ? AND who = ''`,
		who, from, after)
}

// SettingsLog — журнал, новые сверху. `чьи`: пусто — все, «люди» — только
// заявленные человеком, «сам» — то, что Сёма сделал без просьбы.
func (s *Store) SettingsLog(whose string, limit int) ([]SettingChange, error) {
	where := ""
	switch whose {
	case "люди":
		where = ` WHERE who <> ''`
	case "сам":
		where = ` WHERE who = ''`
	}
	return s.settingsLogQuery(where, nil, limit)
}

// CountSettingsLog — сколько правок в журнале: всего и заявленных
// человеком.
func (s *Store) CountSettingsLog() (total, byHumans int) {
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN who <> '' THEN 1 ELSE 0 END), 0)
		FROM settings_log`).Scan(&total, &byHumans)
	return total, byHumans
}

// PurgeSettingsLog — уборка по сроку.
func (s *Store) PurgeSettingsLog(before string) int64 {
	res, err := s.db.Exec(`DELETE FROM settings_log WHERE at < ?`, before)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// logRuleChange — правка правил доступа сервера в том же журнале, что и
// остальные настройки: что разрешили, спросили или запретили, видно там же,
// где всё прочее. Правила лежат не в `settings`, а в своей таблице, поэтому
// дверь `setSetting` их не видит, и писать журнал приходится самому.
func (s *Store) logRuleChange(host, kind, what string) {
	s.logSettingChange(SettingChange{Key: "ssh.rules." + host + "." + kind, After: what})
}

// logMCPRuleChanges — что в правилах сервера MCP поменялось: по строке на
// инструмент, чей ответ стал другим, и строка про ответ для нового.
func (s *Store) logMCPRuleChanges(server string, before map[string]string, defBefore string, after map[string]string, defAfter string) {
	word := map[string]string{RuleAllow: "разрешить", RuleAsk: "спросить", RuleDeny: "запретить", "": "не решён"}
	for tool, now := range after {
		if was := before[tool]; was != now {
			s.logSettingChange(SettingChange{Key: "mcp.rules." + server + "." + tool, Before: word[was], After: word[now]})
		}
	}
	if defBefore != defAfter {
		s.logSettingChange(SettingChange{Key: "mcp.rules." + server + ".новый", Before: word[defBefore], After: word[defAfter]})
	}
}
