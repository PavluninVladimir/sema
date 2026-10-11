package api

// Экран находок проверяется жестом, а не вызовом: собираем страницу,
// находим на ней кнопку и нажимаем её тем же путём, каким это делает
// браузер. Иначе зелёный тест уживается с экраном, где тумблер ничего не
// переключает — так уже было с «Исправить →».

import (
	"strconv"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// actionOnScreen — ищет действие по имени во всех строках и формах.
func actionOnScreen(doc *Document, name string) (Action, bool) {
	var walk func([]Component) (Action, bool)
	walk = func(cs []Component) (Action, bool) {
		for _, c := range cs {
			for _, a := range c.Actions {
				if a.Action == name {
					return a, true
				}
			}
			if c.Submit != nil && c.Submit.Action == name {
				return *c.Submit, true
			}
			if a, ok := walk(c.Children); ok {
				return a, true
			}
		}
		return Action{}, false
	}
	return walk(doc.Components)
}

func TestFindingsScreenIsClickable(t *testing.T) {
	s, _ := testServer(t)

	doc, err := s.buildScreen(watchScreen, "", "", 0)
	if err != nil {
		t.Fatalf("экран не собрался: %v", err)
	}
	for _, name := range []string{"toggle_watch", "toggle_watch_stop", "toggle_watch_disagree", "toggle_watch_doctor", "save_watch_limits"} {
		if _, ok := actionOnScreen(doc, name); !ok {
			t.Fatalf("на экране нет действия %q", name)
		}
	}

	// тумблеры: нажатие меняет состояние, а не молчит
	before := s.eng.WatchStops()
	if _, err := s.execute(actionRequest{Action: "toggle_watch_stop"}); err != nil {
		t.Fatalf("toggle_watch_stop: %v", err)
	}
	if s.eng.WatchStops() == before {
		t.Fatalf("право останавливать не переключилось")
	}
	before = s.eng.WatchDisagree()
	if _, err := s.execute(actionRequest{Action: "toggle_watch_disagree"}); err != nil {
		t.Fatalf("toggle_watch_disagree: %v", err)
	}
	if s.eng.WatchDisagree() == before {
		t.Fatalf("сверка с хранилищем не переключилась")
	}

	// пороги: форма сохраняет то, что в неё написали
	if _, err := s.execute(actionRequest{Action: "save_watch_limits",
		Params: map[string]string{"repeat": "7", "window": "5", "keep": "14"}}); err != nil {
		t.Fatalf("save_watch_limits: %v", err)
	}
	if s.eng.WatchRepeat() != 7 || s.eng.WatchWindow() != 5 {
		t.Fatalf("пороги не сохранились: повтор %d, окно %d", s.eng.WatchRepeat(), s.eng.WatchWindow())
	}
}

// Открытая находка обязана быть видна на экране и вести туда, где чинят.
func TestFindingVisibleAndLeadsSomewhere(t *testing.T) {
	s, st := testServer(t)
	if _, err := st.SaveFinding(store.Finding{
		Key: "проба:1", Kind: "повтор без следа", Ref: "file:7",
		Level: store.FindingAlert, Summary: "работа повторяется впустую",
		Numbers: "12 раз за 3 ч", Measure: store.MeasureAlert, Screen: "queue",
	}); err != nil {
		t.Fatalf("находка не записалась: %v", err)
	}
	doc, err := s.buildScreen(watchScreen, "", "", 0)
	if err != nil {
		t.Fatalf("экран не собрался: %v", err)
	}
	var нашли bool
	var walk func([]Component)
	walk = func(cs []Component) {
		for _, c := range cs {
			if c.Subtitle == "работа повторяется впустую" {
				нашли = true
				var has bool
				for _, a := range c.Actions {
					if a.Action == "$goto" && a.Target == "queue" {
						has = true
					}
				}
				if !has {
					t.Errorf("у находки нет перехода туда, где чинят")
				}
			}
			walk(c.Children)
		}
	}
	walk(doc.Components)
	if !нашли {
		t.Fatalf("открытая находка не показана на экране")
	}
	_ = strconv.Itoa
}
