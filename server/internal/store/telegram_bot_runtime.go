package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type TelegramBotFeatures struct {
	Groups           []int64  `json:"groups"`
	BlockLinks       bool     `json:"block_links"`
	BlockWords       bool     `json:"block_words"`
	Words            []string `json:"words"`
	BlockFlood       bool     `json:"block_flood"`
	Welcome          bool     `json:"welcome"`
	WelcomeText      string   `json:"welcome_text"`
	Commands         bool     `json:"commands"`
	StartText        string   `json:"start_text"`
	HelpText         string   `json:"help_text"`
	PrivateReply     bool     `json:"private_reply"`
	PrivateReplyText string   `json:"private_reply_text"`
}

type TelegramBotState struct {
	WebhookURL   string              `json:"webhook_url"`
	WebhookHash  []byte              `json:"-"`
	Features     TelegramBotFeatures `json:"features"`
	LastDelivery int64               `json:"last_delivery"`
	LastResult   string              `json:"last_result"`
	LastGroup    int64               `json:"last_group_id"`
}

func (s *Store) migrateTelegramBotRuntime() error {
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS telegram_bot_state (
			bot_id INTEGER PRIMARY KEY REFERENCES messenger_bots(id),
			webhook_url TEXT NOT NULL,
			webhook_hash BLOB NOT NULL,
			features TEXT NOT NULL,
			last_delivery INTEGER NOT NULL DEFAULT 0,
			last_group_id INTEGER NOT NULL DEFAULT 0,
			last_result TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS telegram_bot_deliveries (
			bot_id INTEGER NOT NULL REFERENCES messenger_bots(id),
			update_id INTEGER NOT NULL,
			received INTEGER NOT NULL,
			chat_id INTEGER NOT NULL,
			sender_id INTEGER NOT NULL,
			PRIMARY KEY (bot_id, update_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_telegram_bot_delivery_time ON telegram_bot_deliveries(received)`,
		`CREATE INDEX IF NOT EXISTS idx_telegram_bot_delivery_sender ON telegram_bot_deliveries(bot_id, chat_id, sender_id, received)`,
	} {
		if _, err := s.db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) TelegramBotStateFor(botID int64) (TelegramBotState, error) {
	var state TelegramBotState
	var features string
	err := s.db.QueryRow(`SELECT webhook_url, webhook_hash, features, last_delivery, last_result, last_group_id
		FROM telegram_bot_state WHERE bot_id = ?`, botID).Scan(
		&state.WebhookURL, &state.WebhookHash, &features, &state.LastDelivery, &state.LastResult, &state.LastGroup)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	return state, json.Unmarshal([]byte(features), &state.Features)
}

func (s *Store) SaveTelegramBotState(bot MessengerBot, state TelegramBotState) error {
	features, err := json.Marshal(state.Features)
	if err != nil {
		return err
	}
	hash := state.WebhookHash
	if hash == nil {
		hash = []byte{}
	}
	return messengerBotUpdated(s.db.Exec(`INSERT INTO telegram_bot_state(bot_id, webhook_url, webhook_hash, features)
		SELECT id, ?, ?, ? FROM messenger_bots WHERE id = ? AND version = ? AND platform = 'telegram'
		ON CONFLICT(bot_id) DO UPDATE SET webhook_url = excluded.webhook_url,
			webhook_hash = excluded.webhook_hash, features = excluded.features`,
		state.WebhookURL, hash, string(features), bot.ID, bot.version))
}

// ClaimTelegramBotDelivery запоминает доставку до исходящего действия, чтобы Telegram не повторил действие.
func (s *Store) ClaimTelegramBotDelivery(botID, updateID, chat, floodSender int64, hash []byte) (claimed bool, recent int, err error) {
	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO telegram_bot_deliveries(bot_id, update_id, received, chat_id, sender_id)
		SELECT bot_id, ?, ?, ?, ? FROM telegram_bot_state WHERE bot_id = ? AND webhook_hash = ? AND webhook_url <> ''
		ON CONFLICT(bot_id, update_id) DO NOTHING`, updateID, now, chat, floodSender, botID, hash)
	if err != nil {
		return false, 0, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, 0, err
	}
	if _, err := tx.Exec(`DELETE FROM telegram_bot_deliveries WHERE received < ?`, now-48*time.Hour.Milliseconds()); err != nil {
		return false, 0, err
	}
	if floodSender != 0 {
		if err := tx.QueryRow(`SELECT count(*) FROM telegram_bot_deliveries
			WHERE bot_id = ? AND chat_id = ? AND sender_id = ? AND received >= ?`,
			botID, chat, floodSender, now-10*time.Second.Milliseconds()).Scan(&recent); err != nil {
			return false, 0, err
		}
	}
	if _, err := tx.Exec(`UPDATE telegram_bot_state SET last_delivery = ?, last_group_id = COALESCE(NULLIF(?, 0), last_group_id) WHERE bot_id = ?`, now, chat, botID); err != nil {
		return false, 0, err
	}
	return true, recent, tx.Commit()
}

func (s *Store) SetTelegramBotResult(botID int64, message string) error {
	_, err := s.db.Exec(`UPDATE telegram_bot_state SET last_result = ? WHERE bot_id = ?`, message, botID)
	return err
}
