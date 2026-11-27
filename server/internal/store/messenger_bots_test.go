package store

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/secret"
)

func TestMessengerBotTokenLifecycle(t *testing.T) {
	st := testStore(t)
	profile := MessengerBot{Platform: "telegram", ExternalID: 42, Name: "Проба", Status: "ok", CheckedAt: "2026-11-25T00:00:00Z"}
	if _, err := st.AddMessengerBot(profile, "первый-токен"); err == nil {
		t.Fatal("запись токена без ключа шифрования")
	}
	st.UseSecretBox(mustBox(t))
	id, err := st.AddMessengerBot(profile, "первый-токен")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := st.AddMessengerBot(profile, "неявная-замена"); err != nil || again != id {
		t.Fatalf("повторное подключение: %d, %v", again, err)
	}
	profile.Platform = "max"
	if maxID, err := st.AddMessengerBot(profile, "токен-макс"); err != nil || maxID == id {
		t.Fatalf("ID двух платформ не различаются: %d, %v", maxID, err)
	}
	first, err := st.MessengerBot(id)
	if err != nil {
		t.Fatal(err)
	}
	if token, err := st.MessengerBotToken(first); err != nil || token != "первый-токен" {
		t.Fatal("повторное подключение изменило токен")
	}
	if raw, ok := st.rawSecret(first.secretName()); !ok || !secret.Encrypted(raw) {
		t.Fatal("токен записан открытым текстом или отсутствует")
	}
	if _, ok := st.SecretValue(first.secretName()); ok {
		t.Fatal("обычное хранилище отдаёт токен бота по имени")
	}
	if err := st.ReplaceMessengerBotToken(first, "новый-токен"); err != nil {
		t.Fatal(err)
	}
	first.Status = "denied"
	if err := st.SaveMessengerBotCheck(first); err == nil {
		t.Fatal("проверка прежнего токена затёрла результат замены")
	}
	current, err := st.MessengerBot(id)
	if err != nil || current.Status != "ok" {
		t.Fatalf("неверное состояние после замены: %+v, %v", current, err)
	}
	if token, err := st.MessengerBotToken(current); err != nil || token != "новый-токен" {
		t.Fatal("замена токена не сохранилась")
	}
	fresh, err := secret.New("новый-ключ-шифрования-для-ботов")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RotateKey(fresh); err != nil {
		t.Fatal(err)
	}
	if token, err := st.MessengerBotToken(current); err != nil || token != "новый-токен" {
		t.Fatal("токен не читается после смены ключа")
	}
	if err := st.DeleteMessengerBot(current); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.rawSecret(current.secretName()); ok {
		t.Fatal("токен остался после отключения бота")
	}
	if bots, err := st.MessengerBots(); err != nil || len(bots) != 1 || bots[0].Platform != "max" {
		t.Fatalf("отключение затронуло другие подключения: %+v, %v", bots, err)
	}
}
