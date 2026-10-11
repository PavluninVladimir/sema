package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Дверь стоит у выхода и смотрит на собранный промпт целиком. Раньше
// проверка стояла в пяти местах сборки контекста и смотрела на отдельные
// сущности, а текст разговора не смотрел никто — так номер уехал.
func TestGateLooksOnWholePrompt(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	for _, m := range []ModelConf{
		{Name: "дом", Type: "openai", BaseURL: "http://llm:8080/v1", Model: "q", Trusted: true},
		{Name: "облако", Type: "openai", BaseURL: "https://api.example.com/v1", Model: "d"},
	} {
		if err := e.AddModel(m); err != nil {
			t.Fatal(err)
		}
	}
	home := &fakeProvider{answer: "ок"}
	cloud := &fakeProvider{answer: "ок"}
	e.modelMu.Lock()
	e.providers["дом"], e.providers["облако"] = home, cloud
	e.modelMu.Unlock()

	secret := "Сожми разговор: человек прислал паспорт, серия и номер внутри."
	if _, err := e.provider("облако").Complete(context.Background(), secret); err == nil {
		t.Fatal("секретный промпт ушёл недоверенной модели")
	}
	if cloud.calls != 0 {
		t.Fatal("недоверенную модель всё-таки позвали")
	}
	// доверенной можно всё: наружу ничего не уходит
	if _, err := e.provider("дом").Complete(context.Background(), secret); err != nil {
		t.Fatalf("доверенная модель не получила текст: %v", err)
	}
	if home.calls != 1 {
		t.Fatalf("доверенную позвали %d раз", home.calls)
	}
	// обычный текст недоверенной проходит
	if _, err := e.provider("облако").Complete(context.Background(), "какая погода"); err != nil {
		t.Fatalf("обычный текст не прошёл: %v", err)
	}
}

// Отказ объясняет себя человеку: «что-то не понравилось» — не объяснение.
func TestDenialGateExplainsReason(t *testing.T) {
	r := Decision{Class: "секретное", Reason: "слово «паспорт»", Denial: "секретное не отдаётся недоверенной модели"}
	text := GateDenial(r)
	for _, must := range []string{"паспорт", "недоверенной"} {
		if !strings.Contains(text, must) {
			t.Errorf("в отказе нет %q: %s", must, text)
		}
	}
	if GateDenial(Decision{Allow: true}) != "" {
		t.Error("у пропущенного промпта есть текст отказа")
	}
}

var _ = errors.New
