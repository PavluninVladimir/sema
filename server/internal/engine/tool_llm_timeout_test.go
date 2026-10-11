package engine

import (
	"strings"
	"testing"
	"time"
)

// Домашней модели у sema.llm даётся срок задачи модуля, облачной — две
// минуты. Модуль «Серверы под присмотром» четыре ночи подряд упирался в
// две минуты у qwen3 на Raspberry Pi и ни разу не дождался сводки.
func TestDueToolModelHomeLonger(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := e.AddModel(ModelConf{Name: "дома", Type: "openai", BaseURL: "http://127.0.0.1:8080/v1", Model: "q"}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddModel(ModelConf{Name: "облако", Type: "openai", BaseURL: "https://api.example.com/v1", Model: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("modules.task_timeout_s", "540"); err != nil {
		t.Fatal(err)
	}
	if d := e.modelToolTimeout("дома"); d != 540*time.Second {
		t.Errorf("домашней модели дали %s, а должны срок задачи модуля", d)
	}
	if d := e.modelToolTimeout("облако"); d != 2*time.Minute {
		t.Errorf("облачной модели дали %s, а должны две минуты", d)
	}
}

// Промпт наряда говорит модели, чьё это оборудование и по чьему поручению
// она работает: без этого арбитр отказывался писать наряд для роутера —
// пользователи, ключи и NAT в осмотре читались как чужая сеть.
func TestOrderNamesOwner(t *testing.T) {
	for _, word := range []string{"владелец", "собственного", "поручил"} {
		if !strings.Contains(nightPlanPrompt, word) {
			t.Errorf("в промпте наряда нет слова %q", word)
		}
	}
}
