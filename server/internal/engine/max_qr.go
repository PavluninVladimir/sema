package engine

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"

	"github.com/PavluninVladimir/sema/server/internal/max"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

type maxQRState struct {
	mu      sync.Mutex
	attempt *maxQRAttempt
}

type maxQRClient interface {
	PollQR(context.Context, max.QRLogin) (max.QRResult, error)
	ConfirmQRPassword(context.Context, string, string) (string, error)
	Close()
}

type maxQRAttempt struct {
	client    maxQRClient
	qr        max.QRLogin
	device    string
	png       []byte
	timer     *time.Timer
	poll      *time.Timer
	until     time.Time
	challenge string
	nextPoll  time.Time
}

func (s *maxQRState) clear() {
	if attempt := s.attempt; attempt != nil {
		s.attempt = nil
		attempt.timer.Stop()
		if attempt.poll != nil {
			attempt.poll.Stop()
		}
		attempt.client.Close()
	}
}

func (e *Engine) MAXBeginQR(ctx context.Context) error {
	s := &e.maxQR
	s.mu.Lock()
	defer s.mu.Unlock()
	if !e.store.SecretsEncrypted() {
		return fmt.Errorf("для входа в MAX нужен ключ шифрования Сёмы")
	}
	if acc, ok := e.store.MAXAccountGet(); ok && acc.State == "ok" && acc.Token != "" {
		return fmt.Errorf("аккаунт MAX уже подключён")
	}
	s.clear()
	device := uuid.NewString()
	client := max.New(device, "")
	qr, err := client.BeginQR(ctx)
	if err != nil {
		client.Close()
		return err
	}
	img, err := maxQRImage(qr.Link)
	if err != nil {
		client.Close()
		return err
	}
	attempt := &maxQRAttempt{client: client, device: device, qr: qr, png: img}
	s.attempt = attempt
	e.setMAXQRDeadline(attempt, qr.ExpiresAt)
	e.scheduleMAXQRPoll(attempt)
	return nil
}

// Таймеры создаются под блокировкой maxQR.mu.
func (e *Engine) setMAXQRDeadline(attempt *maxQRAttempt, until time.Time) {
	if attempt.timer != nil {
		attempt.timer.Stop()
	}
	attempt.until = until
	attempt.timer = time.AfterFunc(time.Until(until), func() {
		s := &e.maxQR
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.attempt == attempt && attempt.until.Equal(until) {
			s.clear()
			e.Changed("settings:max")
		}
	})
}

func (e *Engine) scheduleMAXQRPoll(attempt *maxQRAttempt) {
	delay := time.Until(attempt.nextPoll)
	if delay <= 0 {
		delay = attempt.qr.PollInterval
	}
	attempt.poll = time.AfterFunc(delay, func() {
		s := &e.maxQR
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.attempt != attempt || attempt.challenge != "" || !time.Now().Before(attempt.until) {
			return
		}
		if time.Now().Before(attempt.nextPoll) {
			e.scheduleMAXQRPoll(attempt)
			return
		}
		deadline := time.Now().Add(12 * time.Second)
		if attempt.until.Before(deadline) {
			deadline = attempt.until
		}
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		changed, err := e.pollMAXQR(ctx, attempt)
		if err != nil {
			s.clear()
		}
		if changed || err != nil {
			e.Changed("settings:max")
			return
		}
		e.scheduleMAXQRPoll(attempt)
	})
}

func maxQRImage(link string) ([]byte, error) {
	matrix, err := qrcode.NewQRCodeWriter().Encode(link, gozxing.BarcodeFormat_QR_CODE, 280, 280, nil)
	if err != nil {
		return nil, fmt.Errorf("не удалось создать изображение QR для MAX")
	}
	img := image.NewGray(image.Rect(0, 0, matrix.GetWidth(), matrix.GetHeight()))
	for y := 0; y < matrix.GetHeight(); y++ {
		for x := 0; x < matrix.GetWidth(); x++ {
			if !matrix.Get(x, y) {
				img.Pix[img.PixOffset(x, y)] = 255
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (e *Engine) MAXQR() ([]byte, time.Time) {
	s := &e.maxQR
	s.mu.Lock()
	defer s.mu.Unlock()
	if attempt := s.attempt; attempt != nil && attempt.challenge == "" && time.Now().Before(attempt.until) {
		return attempt.png, attempt.qr.ExpiresAt
	}
	return nil, time.Time{}
}

func (e *Engine) MAXQRNeedsPassword() bool {
	s := &e.maxQR
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attempt
	return a != nil && a.challenge != "" && time.Now().Before(a.until)
}

func (e *Engine) MAXCancelQR() {
	s := &e.maxQR
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clear()
}

func (e *Engine) MAXPollQR(ctx context.Context) error {
	s := &e.maxQR
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt := s.attempt
	now := time.Now()
	if attempt == nil || !now.Before(attempt.until) {
		s.clear()
		return fmt.Errorf("срок действия QR истёк. Получите новый код")
	}
	if attempt.challenge != "" {
		return nil
	}
	if now.Before(attempt.nextPoll) {
		return fmt.Errorf("подождите несколько секунд перед повторной проверкой")
	}
	changed, err := e.pollMAXQR(ctx, attempt)
	if err != nil {
		s.clear()
		return err
	}
	if !changed {
		return fmt.Errorf("MAX ждёт подтверждения в приложении")
	}
	return nil
}

func (e *Engine) pollMAXQR(ctx context.Context, attempt *maxQRAttempt) (bool, error) {
	attempt.nextPoll = time.Now().Add(attempt.qr.PollInterval)
	result, err := attempt.client.PollQR(ctx, attempt.qr)
	if err != nil {
		return false, err
	}
	if result.ChallengeID != "" {
		attempt.challenge = result.ChallengeID
		attempt.png = nil
		e.setMAXQRDeadline(attempt, time.Now().Add(2*time.Minute))
		return true, nil
	}
	if result.Token != "" {
		return true, e.finishMAXQR(ctx, attempt, result.Token)
	}
	return false, nil
}

func (e *Engine) MAXConfirmQRPassword(ctx context.Context, password string) error {
	if password == "" {
		return fmt.Errorf("введите пароль MAX")
	}
	s := &e.maxQR
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt := s.attempt
	if attempt == nil || attempt.challenge == "" || !time.Now().Before(attempt.until) {
		return fmt.Errorf("проверка пароля истекла. Получите новый QR")
	}
	token, err := attempt.client.ConfirmQRPassword(ctx, attempt.challenge, password)
	if err != nil {
		return err
	}
	return e.finishMAXQR(ctx, attempt, token)
}

func (e *Engine) finishMAXQR(ctx context.Context, attempt *maxQRAttempt, token string) error {
	defer e.maxQR.clear()
	client := max.New(attempt.device, token)
	defer client.Close()
	phone, err := client.Login(ctx)
	if err != nil {
		return err
	}
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return fmt.Errorf("MAX не сообщил номер подключённого аккаунта")
	}
	acc, ok := e.store.MAXAccountGet()
	if ok && strings.TrimPrefix(acc.Phone, "+") != strings.TrimPrefix(phone, "+") {
		return fmt.Errorf("QR подтверждён для другого номера. Сначала отмените прежний вход на экране MAX")
	}
	if !ok {
		acc = &store.MAXAccount{Phone: phone}
	}
	acc.DeviceID = attempt.device
	acc.Token, acc.State, acc.Note = token, "ok", "Вход подтверждён в приложении MAX"
	acc.Last = time.Now().Format(time.RFC3339)
	if err := e.store.SaveMAXAccount(acc); err != nil {
		return err
	}
	_ = e.store.SetSetting(maxCaptchaKey, "")
	_ = e.store.SetSetting(maxVerifyKey, "")
	return nil
}
