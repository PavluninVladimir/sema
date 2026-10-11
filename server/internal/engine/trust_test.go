package engine

import (
	"context"
	"testing"
)

// Доверие — утверждение человека, не адреса: новая модель по своему
// адресу всё равно внешняя, пока её не пометили; разовый перенос делает
// доверенными только те, что были «своими» до появления признака.
func TestTrustModelNotDerivedFromAddr(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.AddModel(ModelConf{Name: "домашняя", Type: "openai", BaseURL: "http://llm:8080/v1", Model: "q"}); err != nil {
		t.Fatal(err)
	}
	if !e.ModelIsCloud("домашняя") {
		t.Fatal("новая модель по своему адресу должна быть внешней, пока человек не доверил")
	}
	if !e.ModelOnThisMachine("домашняя") {
		t.Fatal("для пауз очереди модель по короткому имени в докере — на этой машине")
	}
	if err := e.SetModelTrust("домашняя", true); err != nil {
		t.Fatal(err)
	}
	if e.ModelIsCloud("домашняя") || !e.ModelIsTrusted("домашняя") {
		t.Fatal("после доверия модель должна видеть закрытое")
	}
	if !e.BeforeModel(context.Background(), "домашняя", "паспорт, серия и номер").Allow {
		t.Fatal("доверенной модели можно всё")
	}
	if err := e.SetModelTrust("домашняя", false); err != nil {
		t.Fatal(err)
	}
	if !e.ModelIsCloud("домашняя") {
		t.Fatal("отозванное доверие должно действовать сразу")
	}
	// клиент по подписке доверенным не бывает: уносит текст провайдеру
	if err := e.AddModel(ModelConf{Name: "подписка", Type: "cli", BaseURL: "codex", Model: "codex", Trusted: true}); err != nil {
		t.Fatal(err)
	}
	if e.ModelIsTrusted("подписка") || !e.ModelIsCloud("подписка") {
		t.Fatal("cli не может быть доверенной")
	}
}

func TestOneTimeMoveTrust(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := e.AddModel(ModelConf{Name: "своя", Type: "openai", BaseURL: "http://embed:8080/v1", Model: "q"}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddModel(ModelConf{Name: "чужая", Type: "openai", BaseURL: "https://api.deepseek.com/v1", Model: "d"}); err != nil {
		t.Fatal(err)
	}
	// как будто сервер стартует впервые после обновления
	if err := st.SetSetting(trustSeedKey, ""); err != nil {
		t.Fatal(err)
	}
	e.seedModelTrust()
	if !e.ModelIsTrusted("своя") {
		t.Fatal("модель по своему адресу до появления признака должна остаться доверенной")
	}
	if e.ModelIsTrusted("чужая") {
		t.Fatal("облачная модель доверенной не становится")
	}
	// второй запуск ничего не трогает: новая своя остаётся внешней
	if err := e.AddModel(ModelConf{Name: "ещё своя", Type: "openai", BaseURL: "http://llm:8080/v1", Model: "q"}); err != nil {
		t.Fatal(err)
	}
	e.seedModelTrust()
	if e.ModelIsTrusted("ещё своя") {
		t.Fatal("после переноса доверие ставит только человек")
	}
}

// Деньги и подпись «наружу» — по адресу, дверь — по доверию: доверив
// облачную модель по ошибке, расход всё равно считается.
func TestTrustNotMakesModelFree(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.AddModel(ModelConf{Name: "облако", Type: "openai", BaseURL: "https://api.example.com/v1", Model: "m", Price: "$1 / 1M", Trusted: true}); err != nil {
		t.Fatal(err)
	}
	if !e.ModelIsRemote("облако") {
		t.Fatal("облачная модель по адресу — удалённая, доверие этого не меняет")
	}
	if cost, _ := e.costOf("облако", 1_000_000, 0); cost <= 0 {
		t.Fatalf("доверенная облачная модель стала бесплатной: %v", cost)
	}
}
