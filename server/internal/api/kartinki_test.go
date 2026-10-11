package api

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Картинки к постам идут общим путём: fal.ai подключается в «Подключить
// провайдера», а чем рисуем — видно строкой среди служебных ролей. Отдельного
// раздела со своим ключом нет: он городил второй путь к тому же самому.
func TestDrawingByCommonPath(t *testing.T) {
	s, _ := testServer(t)

	провайдеры, err := s.buildScreen("settings:models~providers", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range провайдеры.Components {
		if c.ID == "картинки" {
			t.Error("остался отдельный раздел «Картинки к постам» — ключ должен вводиться общим путём")
		}
	}

	строка := func() *Component {
		doc, err := s.buildScreen("settings:models", "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		var найти func([]Component) *Component
		найти = func(cs []Component) *Component {
			for i := range cs {
				if cs[i].ID == "image-model" {
					return &cs[i]
				}
				if н := найти(cs[i].Children); н != nil {
					return н
				}
			}
			return nil
		}
		return найти(doc.Components)
	}

	без := строка()
	if без == nil {
		t.Fatal("среди служебных ролей нет строки «Рисование картинок»")
	}
	if len(без.Actions) == 0 || без.Actions[0].Label != "Подключить fal.ai" || без.Actions[0].Target != "settings:models@new" {
		t.Errorf("без ключа строка не ведёт к подключению fal.ai: %+v", без.Actions)
	}

	if err := s.eng.SetProviderKey(engine.FalProvider, "ключ-fal"); err != nil {
		t.Fatal(err)
	}
	с := строка()
	if с == nil || len(с.Actions) == 0 || с.Actions[0].Action != "pick_image_model" {
		t.Errorf("с ключом нет «Подобрать заново»: %+v", с)
	}
}
