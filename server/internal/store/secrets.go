package store

// Секреты в базе: значения шифруются на входе и расшифровываются на выходе.
// Если ключа нет, всё работает как раньше, но Сёма знает, какие записи лежат
// открытым текстом, и говорит об этом в настройках.

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/PavluninVladimir/sema/server/internal/secret"
)

// UseSecretBox включает шифрование секретов.
func (s *Store) UseSecretBox(b *secret.Box) { s.box = b }

// SecretsEncrypted — задан ли ключ шифрования.
func (s *Store) SecretsEncrypted() bool { return s.box != nil }

// seal — значение для записи в базу.
func (s *Store) seal(v string) string {
	if s.box == nil || v == "" {
		return v
	}
	out, err := s.box.Seal(v)
	if err != nil {
		log.Printf("шифрование секрета: %v", err)
		return v
	}
	return out
}

// open — значение для использования. Ошибка означает чужой ключ: тогда
// возвращаем пусто, но исходную запись не трогаем.
func (s *Store) open(v string) string {
	if !secret.Encrypted(v) {
		return v
	}
	// ключа нет вовсе (сервер подняли без SEMA_KEY, а в базе шифр): без этой
	// проверки чтение любого секрета роняло сервер обращением к nil
	if s.box == nil {
		s.setSecretErr("ключ шифрования не задан: значение зашифровано и не читается")
		return ""
	}
	out, err := s.box.Open(v)
	if err != nil {
		s.setSecretErr(err.Error())
		return ""
	}
	s.setSecretErr("")
	return out
}

// setSecretErr — под замком: секреты читают параллельные запросы и фоновая
// сборка индекса настроек, и запись строки без замка — гонка, которая может
// уронить процесс (поймал -race 4 октября 2026)
func (s *Store) setSecretErr(v string) {
	s.secretErrMu.Lock()
	s.secretErr = v
	s.secretErrMu.Unlock()
}

// SecretProblem — почему секреты недоступны (пусто, если всё в порядке).
func (s *Store) SecretProblem() string {
	s.secretErrMu.Lock()
	defer s.secretErrMu.Unlock()
	return s.secretErr
}

// Seal и Open — для тех, кто хранит секреты внутри своих структур
// (настройки ящика, список моделей).
func (s *Store) Seal(v string) string { return s.seal(v) }
func (s *Store) Open(v string) string { return s.open(v) }

// modelSecrets — ключи моделей из списка, добавленного человеком.
func (s *Store) modelSecrets() []string {
	var list []map[string]any
	if err := json.Unmarshal([]byte(s.Setting("models.json", "[]")), &list); err != nil {
		return nil
	}
	var out []string
	for _, m := range list {
		if v, ok := m["api_key"].(string); ok && v != "" {
			out = append(out, v)
		}
	}
	return out
}

// SecretSetting и SetSecretSetting — настройка, которая хранится закрытой.
func (s *Store) SecretSetting(key, def string) string {
	v := s.Setting(key, "")
	if v == "" {
		return def
	}
	return s.open(v)
}

// Правка закрытой настройки помечается секретной на входе, а не узнаётся
// по виду: без SEMA_KEY `seal` возвращает значение как есть, и токен лёг
// бы в журнал изменений открытым текстом.
func (s *Store) SetSecretSetting(key, value string) error {
	return s.setSetting(key, s.seal(value), true)
}

// OpenSecrets — какие секреты лежат открытым текстом. Нужен настройкам,
// чтобы не обещать защиту, которой нет.
func (s *Store) OpenSecrets() []string {
	var out []string
	if v := s.Setting("google.refresh_token", ""); v != "" && !secret.Encrypted(v) {
		out = append(out, "токен доступа к почте")
	}
	for _, c := range s.mailConfs() {
		if c == nil {
			continue
		}
		if c.Password != "" && !secret.Encrypted(c.Password) {
			out = append(out, "пароль ящика")
		}
		if c.Secret != "" && !secret.Encrypted(c.Secret) {
			out = append(out, "секрет OAuth")
		}
	}
	if mem, err := s.settingsWithPrefix(scriptMemoryPrefix); err == nil {
		for _, v := range mem {
			if v != "" && !secret.Encrypted(v) {
				out = append(out, "память скриптов")
				break
			}
		}
	}
	return out
}

// SecretCounts — сколько секретов хранится и сколько из них закрыто.
func (s *Store) SecretCounts() (total, sealed int) {
	values := []string{s.Setting("google.refresh_token", "")}
	for _, c := range s.mailConfs() {
		if c != nil {
			values = append(values, c.Password, c.Secret)
		}
	}
	values = append(values, s.modelSecrets()...)
	for _, v := range values {
		if v == "" {
			continue
		}
		total++
		if secret.Encrypted(v) {
			sealed++
		}
	}
	// хранилище ключей и память скриптов — тем же счётом
	vt, vs := s.countVault()
	return total + vt, sealed + vs
}

// EncryptExistingSecrets переводит открытые записи в зашифрованные — один
// раз, при появлении ключа. Если ключ не подходит к уже закрытым записям,
// не трогаем ничего: лучше сообщить, чем испортить.
func (s *Store) EncryptExistingSecrets() (int, error) {
	if s.box == nil {
		return 0, nil
	}
	n := 0
	if v := s.Setting("google.refresh_token", ""); v != "" && !secret.Encrypted(v) {
		if err := s.SetSecretSetting("google.refresh_token", v); err != nil {
			return n, err
		}
		n++
	}
	for account, c := range s.mailConfs() {
		if c == nil {
			continue
		}
		if secret.Encrypted(c.Password) && s.open(c.Password) == "" {
			return n, fmt.Errorf("ключ шифрования не подходит к секретам ящика %s", account)
		}
		if (c.Password != "" && !secret.Encrypted(c.Password)) ||
			(c.Secret != "" && !secret.Encrypted(c.Secret)) {
			if err := s.SaveMailConf(account, s.MailConf(account)); err != nil {
				return n, err
			}
			n++
		}
	}
	m, err := s.encryptScriptMemory()
	return n + m, err
}

// RotateKey перешифровывает секреты новым ключом. Сначала всё читается
// старым, и только если прочиталось — записывается новым: испортить
// сохранённое хуже, чем отказать.
// vaultCheckKey — проверочная запись: известная строка, зашифрованная
// нынешним ключом. По ней ключ проверяется при старте и фраза — при
// восстановлении, даже когда секретов ещё нет.
// VaultCheckKey — имя проверочной записи (для смены ключа снаружи пакета).
const VaultCheckKey = "vault.check"

const (
	vaultCheckKey  = VaultCheckKey
	vaultCheckText = "сёма·ключ·подходит"
)

// EnsureKeyCheck — завести проверочную запись, если её ещё нет.
func (s *Store) EnsureKeyCheck() error {
	if s.box == nil || s.Setting(vaultCheckKey, "") != "" {
		return nil
	}
	sealed, err := s.box.Seal(vaultCheckText)
	if err != nil {
		return err
	}
	return s.SetSetting(vaultCheckKey, sealed)
}

// KeyMatches — подходит ли нынешний ключ к базе: по проверочной записи,
// а её нет — по первому секрету. Пусто — проверить нечем.
func (s *Store) KeyMatches() (ok bool, known bool) {
	if s.box == nil {
		return false, false
	}
	if v := s.Setting(vaultCheckKey, ""); v != "" {
		out, err := s.box.Open(v)
		return err == nil && out == vaultCheckText, true
	}
	if secs, _ := s.VaultSecrets(); len(secs) > 0 {
		_, ok := s.SecretValue(secs[0].Name)
		return ok, true
	}
	return false, false
}

func (s *Store) RotateKey(fresh *secret.Box) error {
	if fresh == nil {
		return fmt.Errorf("новый ключ пустой")
	}
	type item struct{ key, plain string }
	var plains []item
	if v := s.Setting("google.refresh_token", ""); v != "" {
		out := s.open(v)
		if out == "" && secret.Encrypted(v) {
			return fmt.Errorf("текущие секреты не читаются — проверьте ключ, с которым запущен сервер")
		}
		plains = append(plains, item{"google.refresh_token", out})
	}
	confs := map[string]*MailConf{}
	for account := range s.mailConfs() {
		confs[account] = s.MailConf(account)
	}
	models := s.Setting("models.json", "")
	vaultSecrets, memory, err := s.vaultPlain()
	if err != nil {
		return err
	}

	// сессия Telegram и токен MAX лежат не в хранилище, а в своих таблицах:
	// читаем прежним ключом, пишем новым ниже
	tgs, tgErr := s.TGAccounts()
	mx, mxOK := s.MAXAccountGet()

	old := s.box
	s.box = fresh
	for _, it := range plains {
		if err := s.SetSecretSetting(it.key, it.plain); err != nil {
			s.box = old
			return err
		}
	}
	for account, c := range confs {
		if err := s.SaveMailConf(account, c); err != nil {
			s.box = old
			return err
		}
	}
	if models != "" {
		s.box = old
		reopened := s.reopenModelKeys(models)
		s.box = fresh
		if err := s.SetSetting("models.json", s.sealModelKeys(reopened)); err != nil {
			s.box = old
			return err
		}
	}
	if err := s.vaultReseal(vaultSecrets, memory); err != nil {
		s.box = old
		return err
	}
	if tgErr != nil {
		s.box = old
		return tgErr
	}
	for _, tg := range tgs {
		// перешифровка: сессия прочитана прежним ключом и пишется новым,
		// поэтому проверка «а читается ли лежащая» здесь не годится —
		// лежащая нарочно зашифрована старым
		if err := s.saveTGAccountRow(tg, false); err != nil {
			s.box = old
			return err
		}
	}
	if mxOK && mx != nil {
		if err := s.SaveMAXAccount(mx); err != nil {
			s.box = old
			return err
		}
	}
	return nil
}

// reopenModelKeys и sealModelKeys перекладывают ключи моделей из одного
// ключа шифрования в другой, не разбирая остальную структуру.
func (s *Store) reopenModelKeys(raw string) []map[string]any {
	var list []map[string]any
	_ = json.Unmarshal([]byte(raw), &list)
	for _, m := range list {
		if v, ok := m["api_key"].(string); ok {
			m["api_key"] = s.open(v)
		}
	}
	return list
}

func (s *Store) sealModelKeys(list []map[string]any) string {
	for _, m := range list {
		if v, ok := m["api_key"].(string); ok {
			m["api_key"] = s.seal(v)
		}
	}
	raw, _ := json.Marshal(list)
	return string(raw)
}

// SecretView — то же хранилище настроек, но значения ходят через шифрование.
// Нужно тем, кто хранит токены через общий интерфейс Setting/SetSetting
// (вход в Google): им не надо знать, что под ним лежит закрытое значение.
type SecretView struct{ st *Store }

func (s *Store) Secrets() SecretView { return SecretView{st: s} }

func (v SecretView) Setting(key, def string) string { return v.st.SecretSetting(key, def) }
func (v SecretView) SetSetting(key, value string) error {
	return v.st.SetSecretSetting(key, value)
}

// CheckKey — подходит ли нынешний ключ к тому, что уже лежит в хранилище.
// Пробуем прочитать первый попавшийся секрет: не читается — значит ключ
// чужой, и запись сейчас затрёт настоящие значения. Секретов нет —
// проверять нечего, и это не беда.
func (s *Store) CheckKey() error {
	var name, value string
	err := s.db.QueryRow(`SELECT name, value FROM secrets WHERE value LIKE 'enc:v1:%' LIMIT 1`).Scan(&name, &value)
	if err != nil {
		return errNoSecrets // проверять нечего, и это не беда
	}
	if s.box == nil {
		return fmt.Errorf("ключ шифрования не задан, а в хранилище есть шифрованные значения")
	}
	if _, err := s.box.Open(value); err != nil {
		return fmt.Errorf("секрет %q не читается нынешним ключом", name)
	}
	return nil
}

// errNoSecrets — в хранилище пусто: ключ проверить не на чем. Отдельная
// ошибка, чтобы осмотр не пугал этим человека на свежей установке.
var errNoSecrets = fmt.Errorf("секретов ещё нет")
