package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
)

// Telegram: ключи и сессия живут в хранилище секретов, личные чаты по
// умолчанию чувствительные, без ключа шифрования сессия не пишется.

func TestTGCredsFromVault(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if _, err := e.TGCreds(); err == nil {
		t.Fatal("ключи взялись из ниоткуда")
	}
	if err := e.SaveTGCreds("12345", "хэш-приложения"); err != nil {
		t.Fatalf("сохранение ключей: %v", err)
	}
	creds, err := e.TGCreds()
	if err != nil {
		t.Fatalf("чтение ключей: %v", err)
	}
	if creds.AppID != 12345 || creds.AppHash != "хэш-приложения" {
		t.Errorf("ключи: %+v", creds)
	}
	if err := e.SaveTGCreds("не число", "хэш"); err == nil {
		t.Error("нечисловой api_id принят")
	}
}

// Класс по умолчанию: личное — чувствительное, канал — обычное.
func TestTGKindClass(t *testing.T) {
	cases := map[string]string{"user": store.ClassSensitive, "group": store.ClassSensitive, "channel": store.ClassPlain}
	for kind, want := range cases {
		if got := tg.KindClass(kind); got != want {
			t.Errorf("KindClass(%q) = %q, хотели %q", kind, got, want)
		}
	}
}

// Сессия читается обратно, а выбор человека переживает обновление списка.
func TestTGSessionRoundTrip(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	createTGAccounts(t, st)
	acc := &store.TGAccount{Phone: "+79990000000", State: "ok", Session: "секретная-сессия"}
	if err := st.SaveTGAccount(acc); err != nil {
		t.Fatalf("сохранение аккаунта: %v", err)
	}
	got, ok := st.TGAccount("+79990000000")
	if !ok || got.Session != "секретная-сессия" {
		t.Fatalf("сессия не читается обратно: %q", got.Session)
	}
	// выбор человека переживает обновление списка чатов
	if err := st.SaveTGChat(&store.TGChat{Account: testTGAccount, ID: 7, Title: "Бухгалтерия", Kind: "user", Collect: true, Class: store.ClassSecret}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTGChat(&store.TGChat{Account: testTGAccount, ID: 7, Title: "Бухгалтерия (переименовали)", Kind: "user"}); err != nil {
		t.Fatal(err)
	}
	c, _ := st.TGChat(testTGAccount, 7)
	if !c.Collect || c.Class != store.ClassSecret {
		t.Errorf("выбор человека затёрт обновлением списка: собираем %v, класс %q", c.Collect, c.Class)
	}
	if !strings.Contains(c.Title, "переименовали") {
		t.Errorf("название не обновилось: %q", c.Title)
	}
	_ = e
}
