package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MessengerBot — открытые сведения о подключении. Токен хранится отдельно.
type MessengerBot struct {
	ID, ExternalID int64
	Platform       string
	Name, Username string
	Status         string
	CheckedAt      string
	TelegramAPIURL string
	version        int64
}

const messengerBotFields = `id, external_id, platform, name, username, status, checked_at, telegram_api_url, version`

func (s *Store) migrateMessengerBots() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS messenger_bots (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		external_id INTEGER NOT NULL,
		platform    TEXT NOT NULL CHECK (platform IN ('telegram', 'max')),
		name        TEXT NOT NULL,
		username    TEXT NOT NULL,
		status      TEXT NOT NULL,
		checked_at  TEXT NOT NULL,
		telegram_api_url TEXT NOT NULL DEFAULT '',
		version     INTEGER NOT NULL DEFAULT 1,
		UNIQUE (platform, external_id)
	)`)
	if err != nil {
		return err
	}
	var existing bool
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('messenger_bots') WHERE name = 'telegram_api_url')`).Scan(&existing); err != nil {
		return err
	}
	if !existing {
		_, err = s.db.Exec(`ALTER TABLE messenger_bots ADD COLUMN telegram_api_url TEXT NOT NULL DEFAULT ''`)
	}
	return err
}

func (b MessengerBot) secretName() string { return fmt.Sprintf("messenger-bot/%d", b.ID) }

func (b *MessengerBot) scan(row interface{ Scan(...any) error }) error {
	return row.Scan(&b.ID, &b.ExternalID, &b.Platform, &b.Name, &b.Username, &b.Status, &b.CheckedAt, &b.TelegramAPIURL, &b.version)
}

func (s *Store) MessengerBots() ([]MessengerBot, error) {
	rows, err := s.db.Query(`SELECT ` + messengerBotFields + ` FROM messenger_bots ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bots []MessengerBot
	for rows.Next() {
		var bot MessengerBot
		if err := bot.scan(rows); err != nil {
			return nil, err
		}
		bots = append(bots, bot)
	}
	return bots, rows.Err()
}

func (s *Store) MessengerBot(id int64) (MessengerBot, error) {
	var bot MessengerBot
	err := bot.scan(s.db.QueryRow(`SELECT `+messengerBotFields+` FROM messenger_bots WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return bot, errors.New("бот уже отключён или не найден")
	}
	return bot, err
}

// AddMessengerBot оставляет прежнее подключение при повторной регистрации.
func (s *Store) AddMessengerBot(bot MessengerBot, token string) (int64, error) {
	sealed, err := s.sealMessengerBotToken(token)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	err = tx.QueryRow(`INSERT INTO messenger_bots(platform, external_id, name, username, status, checked_at, telegram_api_url)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(platform, external_id) DO NOTHING RETURNING id`,
		bot.Platform, bot.ExternalID, bot.Name, bot.Username, bot.Status, bot.CheckedAt, bot.TelegramAPIURL).Scan(&bot.ID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRow(`SELECT id FROM messenger_bots WHERE platform = ? AND external_id = ?`, bot.Platform, bot.ExternalID).Scan(&bot.ID)
		return bot.ID, err
	}
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(`INSERT INTO secrets(name, kind, value, note, created, created_by) VALUES (?, ?, ?, ?, ?, ?)`,
		bot.secretName(), SecretMessengerBot, sealed, "Токен бота", time.Now().UTC().Format(time.RFC3339), SecretByCore)
	if err != nil {
		return 0, err
	}
	return bot.ID, tx.Commit()
}

func (s *Store) sealMessengerBotToken(token string) (string, error) {
	if s.box == nil {
		return "", errors.New("для сохранения токена нужен ключ шифрования Сёмы")
	}
	if token == "" {
		return "", errors.New("введите токен бота")
	}
	return s.box.Seal(token)
}

// MessengerBotToken читает токен той версии, которую показывает карточка.
func (s *Store) MessengerBotToken(bot MessengerBot) (string, error) {
	var sealed string
	err := s.db.QueryRow(`SELECT v.value FROM secrets v JOIN messenger_bots b ON b.id = ? AND b.version = ?
		WHERE v.name = ? AND v.kind = ?`, bot.ID, bot.version, bot.secretName(), SecretMessengerBot).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("подключение изменилось, откройте карточку снова")
	}
	if err != nil {
		return "", err
	}
	token := s.open(sealed)
	s.LogSecret(SecretUse{Name: bot.secretName(), Who: "проверка связи с ботом", Granted: token != ""})
	if token == "" {
		return "", errors.New("токен не читается, проверьте ключ шифрования Сёмы")
	}
	return token, nil
}

// SaveMessengerBotCheck сохраняет сведения только для проверенной версии.
func (s *Store) SaveMessengerBotCheck(bot MessengerBot) error {
	return messengerBotUpdated(s.db.Exec(`UPDATE messenger_bots SET name = ?, username = ?, status = ?, checked_at = ?
		WHERE id = ? AND version = ?`, bot.Name, bot.Username, bot.Status, bot.CheckedAt, bot.ID, bot.version))
}

// SaveTelegramBotAPIURL обновляет подключение после проверки токена на новом адресе.
func (s *Store) SaveTelegramBotAPIURL(bot MessengerBot) error {
	return messengerBotUpdated(s.db.Exec(`UPDATE messenger_bots SET telegram_api_url = ?, name = ?, username = ?,
		status = ?, checked_at = ?, version = version + 1 WHERE id = ? AND version = ? AND platform = 'telegram'`,
		bot.TelegramAPIURL, bot.Name, bot.Username, bot.Status, bot.CheckedAt, bot.ID, bot.version))
}

// ReplaceMessengerBotToken записывает сведения и проверенный токен вместе.
func (s *Store) ReplaceMessengerBotToken(bot MessengerBot, token string) error {
	sealed, err := s.sealMessengerBotToken(token)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := messengerBotUpdated(tx.Exec(`UPDATE messenger_bots SET name = ?, username = ?, status = ?, checked_at = ?, version = version + 1
		WHERE id = ? AND version = ?`, bot.Name, bot.Username, bot.Status, bot.CheckedAt, bot.ID, bot.version)); err != nil {
		return err
	}
	var previous string
	if err := tx.QueryRow(`SELECT value FROM secrets WHERE name = ? AND kind = ?`,
		bot.secretName(), SecretMessengerBot).Scan(&previous); err != nil {
		return err
	}
	if s.open(previous) == "" {
		return errors.New("прежний токен не читается, проверьте ключ шифрования Сёмы")
	}
	if err := messengerBotUpdated(tx.Exec(`UPDATE secrets SET value = ? WHERE name = ? AND kind = ?`,
		sealed, bot.secretName(), SecretMessengerBot)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteMessengerBot(bot MessengerBot) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM telegram_bot_deliveries WHERE bot_id = ?`, bot.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM telegram_bot_state WHERE bot_id = ?`, bot.ID); err != nil {
		return err
	}
	if err := messengerBotUpdated(tx.Exec(`DELETE FROM messenger_bots WHERE id = ? AND version = ?`, bot.ID, bot.version)); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM secrets WHERE name = ? AND kind = ?`, bot.secretName(), SecretMessengerBot); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM secret_log WHERE name = ?`, bot.secretName()); err != nil {
		return err
	}
	return tx.Commit()
}

func messengerBotUpdated(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return errors.New("подключение изменилось, откройте карточку снова")
	}
	return err
}
