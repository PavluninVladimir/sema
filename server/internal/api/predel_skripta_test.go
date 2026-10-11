package api

import (
	"testing"
	"time"
)

// Предел скрипта из чата — настройка на экране «Модули», а не 30 секунд в
// коде: превью статьи (поиск, текст моделью, картинка) в них не влезало.
func TestScriptLimitIsConfigurable(t *testing.T) {
	s, _ := testServer(t)
	if got := s.eng.ScriptTimeout(); got < 2*time.Minute {
		t.Fatalf("по умолчанию предел скрипта слишком мал: %s", got)
	}
	if _, err := s.execute(actionRequest{Action: "set_module_task_timeout", Params: map[string]string{"script_timeout": "900"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.eng.ScriptTimeout(); got != 15*time.Minute {
		t.Fatalf("предел скрипта не сохранился: %s", got)
	}
	if s.eng.ModuleTaskTimeout() != 10*time.Minute {
		t.Fatal("сохранение предела скрипта задело предел задач модулей")
	}
	doc, err := s.buildScreen("modules", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	нашли := false
	for _, c := range doc.Components {
		for _, f := range c.Fields {
			if f.Name == "script_timeout" && f.Value == "900" {
				нашли = true
			}
		}
	}
	if !нашли {
		t.Fatal("предела скрипта нет на экране «Модули»")
	}
}
