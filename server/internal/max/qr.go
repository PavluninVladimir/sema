package max

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// Команда проверки пароля при входе в MAX.
const opAuthCheckPassword = 115

// QRLogin — временная попытка входа, которую подтверждает приложение MAX.
type QRLogin struct {
	Link         string
	ExpiresAt    time.Time
	PollInterval time.Duration
	trackID      string
}

func (c *Client) BeginQR(ctx context.Context) (QRLogin, error) {
	if err := c.open(ctx); err != nil {
		return QRLogin{}, err
	}
	raw, err := c.call(ctx, 288, nil)
	if err != nil {
		return QRLogin{}, err
	}
	var r struct {
		TrackID         string `json:"trackId"`
		Link            string `json:"qrLink"`
		ExpiresAt       int64  `json:"expiresAt"`
		PollingInterval int64  `json:"pollingInterval"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return QRLogin{}, fmt.Errorf("MAX вернул неверный ответ на запрос QR")
	}
	link, err := url.Parse(r.Link)
	until := time.UnixMilli(r.ExpiresAt)
	now := time.Now()
	if err != nil || link.Scheme != "https" || link.Host != "max.ru" || link.User != nil ||
		r.TrackID == "" || !now.Before(until) || until.After(now.Add(5*time.Minute)) ||
		r.PollingInterval < 1 || r.PollingInterval > 60000 {
		return QRLogin{}, fmt.Errorf("MAX не выдал действительный QR")
	}
	return QRLogin{
		Link: r.Link, ExpiresAt: until, trackID: r.TrackID,
		PollInterval: time.Duration(r.PollingInterval) * time.Millisecond,
	}, nil
}

type QRResult struct {
	Token       string
	ChallengeID string
}

// PollQR возвращает результат после подтверждения в приложении MAX.
func (c *Client) PollQR(ctx context.Context, q QRLogin) (QRResult, error) {
	params := map[string]string{"trackId": q.trackID}
	raw, err := c.call(ctx, 289, params)
	if err != nil {
		return QRResult{}, err
	}
	var r struct {
		Status *struct {
			LoginAvailable bool `json:"loginAvailable"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Status == nil {
		return QRResult{}, fmt.Errorf("MAX вернул неверное состояние QR")
	}
	if !r.Status.LoginAvailable {
		return QRResult{}, nil
	}
	raw, err = c.call(ctx, 291, params)
	if err != nil {
		return QRResult{}, err
	}
	return decodeQRResult(raw)
}

func decodeQRResult(raw json.RawMessage) (QRResult, error) {
	var result struct {
		TokenAttrs struct {
			Login struct {
				Token string `json:"token"`
			} `json:"LOGIN"`
		} `json:"tokenAttrs"`
		PasswordChallenge struct {
			TrackID string `json:"trackId"`
		} `json:"passwordChallenge"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return QRResult{}, fmt.Errorf("MAX вернул неверный результат QR-входа")
	}
	if token := result.TokenAttrs.Login.Token; token != "" {
		return QRResult{Token: token}, nil
	}
	if id := result.PasswordChallenge.TrackID; id != "" {
		return QRResult{ChallengeID: id}, nil
	}
	return QRResult{}, fmt.Errorf("MAX не выдал сессию для подтверждённого QR")
}

func (c *Client) ConfirmQRPassword(ctx context.Context, challengeID, password string) (string, error) {
	raw, err := c.call(ctx, opAuthCheckPassword, map[string]string{"trackId": challengeID, "password": password})
	if err != nil {
		// Текст ответа на запрос с паролем не передаём в UI или журнал.
		return "", fmt.Errorf("MAX не принял пароль. Проверьте его и повторите ввод")
	}
	result, err := decodeQRResult(raw)
	if err != nil || result.Token == "" {
		return "", fmt.Errorf("MAX не подтвердил вход по паролю")
	}
	return result.Token, nil
}
