package store

// Хранилище ключей и секретов: ключи SSH, пароли, токены. Значение
// шифруется на входе и никогда не возвращается экрану — только модулю, у
// которого есть право на это имя, и тому в момент обращения. Каждое
// обращение остаётся в журнале: иначе не заметить модуль, который лезет не
// за своим.

import (
	"fmt"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/secret"
)

// Виды секретов: у ключа SSH есть публичная часть, у остальных — нет.
const (
	SecretSSH      = "ssh"
	SecretToken    = "token"
	SecretPassword = "password"
	SecretOther    = "other"
	// SecretOAuth — токены входа на чужой сервер MCP (`mcp:<имя>:oauth`).
	// Ими пользуется только ядро: модулю такой секрет не выдаётся никогда,
	// как приватная часть ключа SSH, а руками он не заменяется — это JSON,
	// который пишет и читает только вход.
	SecretOAuth = "oauth"
)

type Secret struct {
	Name    string
	Kind    string
	Public  string // публичная часть ключа SSH; у остальных пусто
	Note    string // откуда взялся: «сгенерирован», «импортирован»
	Created string
	// CreatedBy — кто завёл запись: SecretByHuman (человек, с экрана «Ключи и
	// секреты» или кнопкой «Подключить хозяина») или SecretByCore (всё остальное:
	// ключ провайдера, пароль ящика, сертификаты агентов, ключи пушей…). Пусто
	// читается как «завёл сам Сёма»: человеческий путь называет себя явно, а
	// новый путь ядра, забывший назвать себя, не получит «Удалить» вровень с
	// вашими ключами
	CreatedBy string
}

// Кто завёл секрет.
const (
	SecretByHuman = "human"
	SecretByCore  = "core"
)

// Service — служебный секрет: его завёл сам Сёма для своих дел, и живёт он у
// своего владельца (ключ провайдера — у провайдера, пароль ящика — у ящика).
// На экране «Ключи и секреты» он лежит отдельно от ваших и без «Удалить».
func (s *Secret) Service() bool { return s.CreatedBy != SecretByHuman }

// SecretUse — одна строка журнала.
type SecretUse struct {
	Name    string
	Module  string // имя модуля, который обращался (пусто — примитив ядра)
	Who     string // как назвать человеку: «задача «Проверка дисков»»
	At      string
	Granted bool
	Note    string
	// Change — это не обращение к значению, а правка записи человеком: заведён,
	// значение заменено, выдан модулю, отозван, удалён. Журнал один, но
	// чтение и правка — разные вопросы, и отбор на экране их различает
	Change bool
}

func (s *Store) migrateVault() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS secrets (
			name    TEXT PRIMARY KEY,
			kind    TEXT NOT NULL,
			value   TEXT NOT NULL,
			public  TEXT NOT NULL DEFAULT '',
			note    TEXT NOT NULL DEFAULT '',
			created TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS secret_log (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			name    TEXT NOT NULL,
			module  TEXT NOT NULL DEFAULT '',
			who     TEXT NOT NULL,
			at      TEXT NOT NULL,
			granted INTEGER NOT NULL,
			note    TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_secret_log_name ON secret_log(name, id DESC)`,
		// кто завёл секрет: у записей, лежавших до этого столбца, он пуст
		`ALTER TABLE secrets ADD COLUMN created_by TEXT NOT NULL DEFAULT ''`,
		// правка записи человеком, а не обращение к значению
		`ALTER TABLE secret_log ADD COLUMN change INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			// ALTER на уже добавленном столбце — не беда: миграция идёт на
			// каждом старте
			if strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return err
		}
	}
	return s.fillSecretCreatedBy()
}

// Заметки, которые пишут только человеческие пути: «добавлен вручную»
// (экран, токен или пароль), «сгенерирован Сёмой» (кнопка человека) и
// «импортирован…» (его приватный ключ). Ядро для своих секретов пишет другие
// заметки, и пустой заметки у него нет вовсе.
const (
	SecretNoteAdded     = "добавлен вручную"
	SecretNoteGenerated = "сгенерирован Сёмой"
	SecretNoteImported  = "импортирован — этот ключ живёт и где-то ещё"
)

// fillSecretCreatedBy — один раз заполнить «кто завёл» у записей, лежавших до
// появления этого признака. Судим по заметке, которую пишет путь создания, а не
// по приставке имени: приставок («model:», «mail:») у ядра много и будет
// больше, а человеческих путей ровно три. Остальные — служебные.
func (s *Store) fillSecretCreatedBy() error {
	_, err := s.db.Exec(`UPDATE secrets SET created_by = CASE
			WHEN note IN (?, ?) OR note LIKE ? THEN ? ELSE ? END
		WHERE created_by = ''`,
		SecretNoteAdded, SecretNoteGenerated, "импортирован%", SecretByHuman, SecretByCore)
	return err
}

// VaultSecrets — список хранилища без значений, по имени.
func (s *Store) VaultSecrets() ([]*Secret, error) {
	rows, err := s.db.Query(`SELECT name, kind, public, note, created, created_by FROM secrets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Secret
	for rows.Next() {
		sec := &Secret{}
		if err := rows.Scan(&sec.Name, &sec.Kind, &sec.Public, &sec.Note, &sec.Created, &sec.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

func (s *Store) Secret(name string) (*Secret, bool) {
	sec := &Secret{}
	err := s.db.QueryRow(`SELECT name, kind, public, note, created, created_by FROM secrets WHERE name = ?`, name).
		Scan(&sec.Name, &sec.Kind, &sec.Public, &sec.Note, &sec.Created, &sec.CreatedBy)
	return sec, err == nil
}

// SaveSecret добавляет или заменяет секрет. Без ключа шифрования отказываем:
// класть пароли открытым текстом хуже, чем не класть вовсе.
func (s *Store) SaveSecret(sec *Secret, value string) error {
	if s.box == nil {
		return fmt.Errorf("ключ шифрования не задан: запустите сервер с переменной окружения, " +
			"иначе секрет лёг бы в базу открытым текстом")
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("значение секрета пустое")
	}
	// уже лежащий секрет, который не читается нынешним ключом, не
	// перезаписывается: сервер, поднятый с чужим SEMA_KEY, иначе молча
	// затирал корень агентов и сессию Telegram — так и случилось
	if old, ok := s.rawSecret(sec.Name); ok && secret.Encrypted(old) && s.open(old) == "" {
		return fmt.Errorf("секрет %q уже есть, но не читается этим ключом — "+
			"перезапись отклонена: проверьте SEMA_KEY, с которым запущен сервер", sec.Name)
	}
	sealed, err := s.box.Seal(value)
	if err != nil {
		return err
	}
	if sec.Created == "" {
		sec.Created = time.Now().Format(time.RFC3339)
	}
	// кто завёл, называет путь создания; не назвал — это дело ядра. Заменяя
	// значение, завёдшего не меняем: первый остаётся первым
	if sec.CreatedBy == "" {
		sec.CreatedBy = SecretByCore
	}
	_, err = s.db.Exec(`
INSERT INTO secrets (name, kind, value, public, note, created, created_by) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET kind = excluded.kind, value = excluded.value,
	public = excluded.public, note = excluded.note`,
		sec.Name, sec.Kind, sealed, sec.Public, sec.Note, sec.Created, sec.CreatedBy)
	return err
}

// rawSecret — значение как оно лежит в базе, без расшифровки.
func (s *Store) rawSecret(name string) (string, bool) {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM secrets WHERE name = ?`, name).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

// ReplaceSecretValue меняет только значение, вид и запись остаются.
func (s *Store) ReplaceSecretValue(name, value string) error {
	sec, ok := s.Secret(name)
	if !ok {
		return fmt.Errorf("секрета %q нет", name)
	}
	return s.SaveSecret(sec, value)
}

// SetSecretNote меняет заметку записи: «для чего он». Значение не трогает.
func (s *Store) SetSecretNote(name, note string) error {
	res, err := s.db.Exec(`UPDATE secrets SET note = ? WHERE name = ?`, strings.TrimSpace(note), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("секрета %q нет", name)
	}
	return nil
}

func (s *Store) DeleteSecret(name string) error {
	if _, err := s.db.Exec(`DELETE FROM secrets WHERE name = ?`, name); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM secret_log WHERE name = ?`, name)
	return err
}

// SecretValue — расшифрованное значение. Единственный путь к нему; вызывать
// только из места, которое пишет в журнал.
func (s *Store) SecretValue(name string) (string, bool) {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM secrets WHERE name = ?`, name).Scan(&v); err != nil {
		return "", false
	}
	out := s.open(v)
	return out, out != ""
}

// LogSecret записывает обращение или правку (use.Change). Журнал не должен
// расти бесконечно: у одного секрета храним последние keepSecretLog записей
// каждого рода — иначе частые чтения вытеснили бы из журнала единственную
// запись о том, кто и когда заменил значение.
func (s *Store) LogSecret(use SecretUse) {
	if use.At == "" {
		use.At = time.Now().Format(time.RFC3339)
	}
	granted, change := 0, 0
	if use.Granted {
		granted = 1
	}
	if use.Change {
		change = 1
	}
	_, _ = s.db.Exec(`INSERT INTO secret_log (name, module, who, at, granted, note, change) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		use.Name, use.Module, use.Who, use.At, granted, use.Note, change)
	_, _ = s.db.Exec(`DELETE FROM secret_log WHERE name = ? AND change = ? AND id NOT IN (
		SELECT id FROM secret_log WHERE name = ? AND change = ? ORDER BY id DESC LIMIT ?)`,
		use.Name, change, use.Name, change, keepSecretLog)
}

const keepSecretLog = 200

// Отборы журнала секретов: чипы вкладки «Журнал». Слова отбора — договор между
// экраном и хранилищем, как у журнала команд.
const (
	SecretLogAll     = ""        // всё подряд
	SecretLogReads   = "reads"   // чтения: значение выдано тому, кто просил
	SecretLogDenied  = "denied"  // отказы: право не выдано, секрета нет, ключ не подошёл
	SecretLogChanges = "changes" // изменения: заведён, значение заменено, выдан, отозван, удалён
)

// secretLogWhere — условие отбора журнала по секрету (пустое имя — по всем) и
// отбору.
func secretLogWhere(name, filter string) (string, []any) {
	var conds []string
	var args []any
	if name != "" {
		conds = append(conds, `name = ?`)
		args = append(args, name)
	}
	switch filter {
	case SecretLogReads:
		conds = append(conds, `change = 0 AND granted = 1`)
	case SecretLogDenied:
		conds = append(conds, `change = 0 AND granted = 0`)
	case SecretLogChanges:
		conds = append(conds, `change = 1`)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return ` WHERE ` + strings.Join(conds, ` AND `), args
}

// SecretLogFiltered — журнал секрета (пустое имя — всех) с отбором, свежие
// первыми: и обращения, и правки.
func (s *Store) SecretLogFiltered(name, filter string, limit int) ([]SecretUse, error) {
	where, args := secretLogWhere(name, filter)
	rows, err := s.db.Query(`SELECT name, module, who, at, granted, note, change FROM secret_log`+
		where+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretUse
	for rows.Next() {
		var u SecretUse
		var granted, change int
		if err := rows.Scan(&u.Name, &u.Module, &u.Who, &u.At, &granted, &u.Note, &change); err != nil {
			return nil, err
		}
		u.Granted, u.Change = granted == 1, change == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountSecretLogFiltered — сколько записей подходит под отбор: число у чипа.
func (s *Store) CountSecretLogFiltered(name, filter string) int {
	where, args := secretLogWhere(name, filter)
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM secret_log`+where, args...).Scan(&n)
	return n
}

// SecretLog — история обращений к значению, новые сверху. Правки записи здесь
// не считаются: «когда брали в последний раз» отвечает на вопрос про чтение.
func (s *Store) SecretLog(name string, limit int) ([]SecretUse, error) {
	rows, err := s.db.Query(`SELECT name, module, who, at, granted, note FROM secret_log
		WHERE name = ? AND change = 0 ORDER BY id DESC LIMIT ?`, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretUse
	for rows.Next() {
		var u SecretUse
		var granted int
		if err := rows.Scan(&u.Name, &u.Module, &u.Who, &u.At, &granted, &u.Note); err != nil {
			return nil, err
		}
		u.Granted = granted == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// LastSecretUse — последнее обращение (nil, если не брали ни разу).
func (s *Store) LastSecretUse(name string) *SecretUse {
	list, err := s.SecretLog(name, 1)
	if err != nil || len(list) == 0 {
		return nil
	}
	return &list[0]
}

// LastSecretChange — последняя правка записи с этой пометкой («значение
// заменено»); nil, если такой не было.
func (s *Store) LastSecretChange(name, note string) *SecretUse {
	list, err := s.SecretLogFiltered(name, SecretLogChanges, 50)
	if err != nil {
		return nil
	}
	for i := range list {
		if list[i].Note == note {
			return &list[i]
		}
	}
	return nil
}

// SecretDenials — сколько раз отказывали и кому в последний раз.
func (s *Store) SecretDenials(name string) (count int, last *SecretUse) {
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM secret_log WHERE name = ? AND change = 0 AND granted = 0`, name).Scan(&count)
	if count == 0 {
		return 0, nil
	}
	var u SecretUse
	var granted int
	err := s.db.QueryRow(`SELECT name, module, who, at, granted, note FROM secret_log
		WHERE name = ? AND change = 0 AND granted = 0 ORDER BY id DESC LIMIT 1`, name).
		Scan(&u.Name, &u.Module, &u.Who, &u.At, &granted, &u.Note)
	if err != nil {
		return count, nil
	}
	return count, &u
}

// OrphanSecretRequests — последние обращения к именам, которых в хранилище
// нет: по одному на имя. Правки записи — не просьбы: удалённый секрет, о
// котором осталась запись «удалён», просителем не считается.
func (s *Store) OrphanSecretRequests() ([]SecretUse, error) {
	rows, err := s.db.Query(`SELECT l.name, l.module, l.who, l.at FROM secret_log l
		WHERE l.id IN (SELECT MAX(id) FROM secret_log WHERE change = 0 GROUP BY name)
		AND l.name NOT IN (SELECT name FROM secrets) ORDER BY l.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretUse
	for rows.Next() {
		var u SecretUse
		if err := rows.Scan(&u.Name, &u.Module, &u.Who, &u.At); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Память скриптов (store.get/set) хранится тем же закрытым слоем: в ней
// могут оказаться токены, и запрещать это словами — не защита.
const scriptMemoryPrefix = "script."

func (s *Store) ScriptMemory(key string) string {
	return s.SecretSetting(scriptMemoryPrefix+key, "")
}

func (s *Store) SetScriptMemory(key, value string) error {
	return s.SetSecretSetting(scriptMemoryPrefix+key, value)
}

// settingsWithPrefix — пары ключ/значение настроек по префиксу, как лежат.
func (s *Store) settingsWithPrefix(prefix string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings WHERE key LIKE ?`, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// encryptScriptMemory закрывает записи памяти скриптов, лежащие открыто.
func (s *Store) encryptScriptMemory() (int, error) {
	all, err := s.settingsWithPrefix(scriptMemoryPrefix)
	if err != nil {
		return 0, err
	}
	n := 0
	for k, v := range all {
		if v == "" || secret.Encrypted(v) {
			continue
		}
		if err := s.SetSecretSetting(k, v); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// vaultPlain читает все секреты хранилища и память скриптов текущим ключом —
// для смены ключа. Ошибка — если хоть что-то не читается.
func (s *Store) vaultPlain() (secrets map[string]string, memory map[string]string, err error) {
	rows, qerr := s.db.Query(`SELECT name, value FROM secrets`)
	if qerr != nil {
		return nil, nil, qerr
	}
	defer rows.Close()
	secrets = map[string]string{}
	for rows.Next() {
		var name, v string
		if err := rows.Scan(&name, &v); err != nil {
			return nil, nil, err
		}
		plain := s.open(v)
		if plain == "" && v != "" {
			return nil, nil, fmt.Errorf("секрет %q не читается текущим ключом", name)
		}
		secrets[name] = plain
	}
	raw, err := s.settingsWithPrefix(scriptMemoryPrefix)
	if err != nil {
		return nil, nil, err
	}
	memory = map[string]string{}
	for k, v := range raw {
		plain := s.open(v)
		if plain == "" && v != "" {
			return nil, nil, fmt.Errorf("память скрипта %q не читается текущим ключом", k)
		}
		memory[k] = plain
	}
	return secrets, memory, nil
}

// vaultReseal записывает секреты и память скриптов текущим (уже новым) ключом.
func (s *Store) vaultReseal(secrets, memory map[string]string) error {
	for name, plain := range secrets {
		sealed, err := s.box.Seal(plain)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE secrets SET value = ? WHERE name = ?`, sealed, name); err != nil {
			return err
		}
	}
	for k, plain := range memory {
		if err := s.SetSecretSetting(k, plain); err != nil {
			return err
		}
	}
	return nil
}

// countVault — сколько значений хранилища и памяти скриптов, и сколько
// из них закрыто.
func (s *Store) countVault() (total, sealed int) {
	rows, err := s.db.Query(`SELECT value FROM secrets`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var v string
			if rows.Scan(&v) == nil && v != "" {
				total++
				if secret.Encrypted(v) {
					sealed++
				}
			}
		}
	}
	if mem, err := s.settingsWithPrefix(scriptMemoryPrefix); err == nil {
		for _, v := range mem {
			if v == "" {
				continue
			}
			total++
			if secret.Encrypted(v) {
				sealed++
			}
		}
	}
	return total, sealed
}
