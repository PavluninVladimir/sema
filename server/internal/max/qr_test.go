package max

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestQRRequiresConfirmation(t *testing.T) {
	srv, conn := twoEnds(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := &Client{conn: conn, waiting: map[int]chan frame{}}
	go func() { _ = c.read(ctx) }()

	serve := func(opcode, cmd int, response any) error {
		kind, data, err := srv.Read(ctx)
		if err != nil {
			return err
		}
		f, err := decodeFrame(kind, data)
		if err != nil || f.Opcode != opcode {
			return fmt.Errorf("ожидался опкод %d, пришёл %d: %v", opcode, f.Opcode, err)
		}
		if (opcode == 289 || opcode == 291) && string(f.Payload) != `{"trackId":"fixture"}` {
			return fmt.Errorf("потеряна привязка к QR")
		}
		if opcode == 115 && !strings.Contains(string(f.Payload), `"trackId":"password-check"`) {
			return fmt.Errorf("потеряна привязка проверки пароля")
		}
		data, err = encodeFrame(cmd, f.Seq, opcode, response)
		if err != nil {
			return err
		}
		return srv.Write(ctx, websocket.MessageBinary, data)
	}
	serverResult := make(chan error, 1)
	go func() {
		for _, reply := range []struct {
			opcode int
			cmd    int
			body   any
		}{
			{288, cmdReply, map[string]any{"trackId": "fixture", "qrLink": "https://max.ru/fixture",
				"expiresAt": time.Now().Add(time.Minute).UnixMilli(), "pollingInterval": 5000}},
			{289, cmdReply, map[string]any{"status": map[string]bool{"loginAvailable": false}}},
			{289, cmdReply, map[string]any{"status": map[string]bool{"loginAvailable": true}}},
			{291, cmdReply, map[string]any{"passwordChallenge": map[string]string{"trackId": "password-check"}}},
			{115, cmdError, map[string]string{"error": "rejected: fixture-wrong-password"}},
			{115, cmdReply, map[string]any{"tokenAttrs": map[string]any{"LOGIN": map[string]string{"token": "fixture-token"}}}},
			{opLogin, cmdReply, map[string]any{"profile": map[string]any{"contact": map[string]any{
				"id": 42, "phone": int64(70000000000),
			}}}},
		} {
			if err := serve(reply.opcode, reply.cmd, reply.body); err != nil {
				serverResult <- err
				return
			}
		}
		serverResult <- nil
	}()

	qr, err := c.BeginQR(ctx)
	if err != nil || qr.Link != "https://max.ru/fixture" || qr.PollInterval != 5*time.Second {
		t.Fatalf("QR не получен: %v", err)
	}
	if result, err := c.PollQR(ctx, qr); err != nil || result != (QRResult{}) {
		t.Fatalf("вход завершился до подтверждения: %v", err)
	}
	result, err := c.PollQR(ctx, qr)
	if err != nil || result.Token != "" || result.ChallengeID != "password-check" {
		t.Fatalf("не получен второй фактор: %v", err)
	}
	if token, err := c.ConfirmQRPassword(ctx, result.ChallengeID, "fixture-wrong-password"); token != "" ||
		err == nil || strings.Contains(err.Error(), "fixture-wrong-password") {
		t.Fatal("отклонённый пароль попал в результат")
	}
	token, err := c.ConfirmQRPassword(ctx, result.ChallengeID, "fixture-password")
	if err != nil || token != "fixture-token" {
		t.Fatal("после повторного ввода сессия не получена")
	}
	c.token = token
	if phone, err := c.Login(ctx); err != nil || phone != "+70000000000" {
		t.Fatalf("номер профиля не восстановлен: %v", err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}
