package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Сведение памяти с секретным в тексте не уходит внешней модели:
// доверенной нет — отказ с советом, и запрос виден как отказ, а не ушёл.
func TestMemoryNotTakesSecretOutside(t *testing.T) {
	fp := &fakeProvider{answer: "{}"}
	e, _ := newTestEngine(t, "облако", fp)
	_ = e.SetModelTrust("облако", false)
	if !e.ModelIsCloud("облако") {
		t.Fatal("тестовая модель должна быть внешней")
	}
	_ = e.SetRoleField(RoleMemory, "model", "облако")
	before := fp.calls
	_, _, err := e.askMemory(context.Background(), "Сожми сессию: человек прислал скан, там паспорт и серия и номер.")
	if err == nil || !strings.Contains(err.Error(), "доверенную") {
		t.Fatalf("секретное ушло бы наружу: %v", err)
	}
	if fp.calls != before {
		t.Fatal("внешняя модель получила секретный текст")
	}
	// обычный текст идёт как раньше
	if _, _, err := e.askMemory(context.Background(), "Сожми сессию: чинили вёрстку списка ролей."); err != nil {
		t.Fatalf("обычный текст не прошёл: %v", err)
	}
}

// Доверенная основная с облачной запасной: на секретном тексте отказ
// основной не должен уводить текст запасной наружу.
func TestSpareModelNotBypassesGate(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	for _, m := range []ModelConf{
		{Name: "дом", Type: "openai", BaseURL: "http://llm:8080/v1", Model: "q", Trusted: true},
		{Name: "облако", Type: "openai", BaseURL: "https://api.example.com/v1", Model: "d"},
	} {
		if err := e.AddModel(m); err != nil {
			t.Fatal(err)
		}
	}
	home := &fakeProvider{err: errors.New("модель не отвечает")}
	cloud := &fakeProvider{answer: "{}"}
	e.modelMu.Lock()
	e.providers["дом"], e.providers["облако"] = home, cloud
	e.modelMu.Unlock()
	_ = e.SetRoleField(RoleMemory, "model", "дом")
	_ = e.SetRoleField(RoleMemory, "spare", "облако")
	_, _, err := e.askMemory(context.Background(), "Сожми: в письме паспорт, серия и номер.")
	if err == nil {
		t.Fatal("отказ основной должен остаться отказом")
	}
	if cloud.calls != 0 {
		t.Fatal("секретный текст ушёл облачной запасной")
	}
	if home.calls == 0 {
		t.Fatal("доверенная основная даже не звалась")
	}
}
