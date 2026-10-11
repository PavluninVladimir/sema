package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Каждый провайдер — свой свёрнутый раздел, даже с одной моделью. Одиночная
// строка стояла между свёрнутыми разделами и читалась как их содержимое:
// «embed:8080 · 2 модели» свёрнут, а qwen3 с адресом llm:8080 видна под ним
// (17 сентября 2026).
func TestModelAlwaysInProviderSection(t *testing.T) {
	s, _ := testServer(t)
	for name, url := range map[string]string{"bge": "http://embed:8080/v1", "bge-small": "http://embed:8080/v1", "qwen3": "http://llm:8080/v1"} {
		if err := s.eng.AddModel(engine.ModelConf{Name: name, Type: "openai", BaseURL: url, Model: name}); err != nil {
			t.Fatal(err)
		}
	}
	// и одинокая модель чужого шлюза: ей тоже нужен свой раздел
	if err := s.eng.AddModel(engine.ModelConf{Name: "одна", Type: "openai", BaseURL: "https://gw.example.org/v1", Model: "одна"}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("settings:models~list", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var list *Component
	for i := range doc.Components {
		if doc.Components[i].ID == "models-list" {
			list = &doc.Components[i]
		}
	}
	if list == nil {
		t.Fatal("в списке моделей нет самого списка")
	}
	rows := 0
	for _, c := range list.Children {
		if c.Type != "section" {
			t.Errorf("элемент %q стоит в списке вне раздела провайдера — его читают как содержимое соседнего", c.ID)
			continue
		}
		if !c.Collapsed {
			t.Errorf("раздел %q не свёрнут: открытость решает Expanded, а не отсутствие свёртки", c.ID)
		}
		for _, r := range c.Children {
			if strings.HasPrefix(r.ID, "model-") {
				rows++
			}
		}
	}
	// «на этой машине»: тестовая модель, qwen3 и два эмбеддера; и отдельно «одна»
	if rows < 5 || len(list.Children) < 2 {
		t.Errorf("моделей в разделах %d, разделов %d", rows, len(list.Children))
	}
}
