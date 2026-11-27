package engine

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/max"
)

type qrPasswordFixture struct {
	polls int
}

func (c *qrPasswordFixture) PollQR(context.Context, max.QRLogin) (max.QRResult, error) {
	c.polls++
	if c.polls == 1 {
		return max.QRResult{}, nil
	}
	return max.QRResult{ChallengeID: "fixture"}, nil
}

func (c *qrPasswordFixture) ConfirmQRPassword(context.Context, string, string) (string, error) {
	return "", errors.New("пароль отклонён")
}

func (c *qrPasswordFixture) Close() {}

func TestMAXQRAutomaticallyShowsPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e := &Engine{live: newLiveHub()}
	t.Cleanup(e.MAXCancelQR)
	events := e.live.subscribe(ctx, time.Millisecond)
	c := &qrPasswordFixture{}
	a := &maxQRAttempt{client: c, qr: max.QRLogin{PollInterval: 10 * time.Millisecond}, png: []byte{1}}
	e.maxQR.mu.Lock()
	e.maxQR.attempt = a
	e.setMAXQRDeadline(a, time.Now().Add(time.Second))
	e.scheduleMAXQRPoll(a)
	e.maxQR.mu.Unlock()

	select {
	case changed := <-events:
		if !slices.Contains(changed, "settings:max") || !e.MAXQRNeedsPassword() {
			t.Fatalf("после подтверждения нет формы пароля: %v", changed)
		}
	case <-ctx.Done():
		t.Fatal("Сёма сама не проверила QR")
	}
	if img, _ := e.MAXQR(); img != nil {
		t.Fatal("QR ещё доступен после запроса пароля")
	}
	if err := e.MAXConfirmQRPassword(ctx, "fixture"); err == nil || !e.MAXQRNeedsPassword() {
		t.Fatal("отклонённый пароль сбросил проверку")
	}
	e.maxQR.mu.Lock()
	defer e.maxQR.mu.Unlock()
	if c.polls != 2 {
		t.Fatalf("проверка QR не остановилась: %d", c.polls)
	}
}
