package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// поручение в чате: карточка плана в переписке, как её кладёт движок
func assignFixture(t *testing.T, s *Server) (chat int64, a *store.Assignment) {
	t.Helper()
	st := s.eng.Store()
	chat, err := st.CreateChat("Дневник")
	if err != nil {
		t.Fatal(err)
	}
	a = &store.Assignment{Chat: chat, Goal: "модуль электронного дневника", State: store.AssignActive}
	if _, err := st.AddAssignment(a, []string{"найти клиент", "написать модуль", "проба на одном ребёнке"}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishAssignStep(a.ID, 1, store.StepDone, "клиент example/school-api"); err != nil {
		t.Fatal(err)
	}
	for _, card := range []engine.ToolCard{
		{Title: "Поручение", Assign: &engine.AssignCard{ID: a.ID}},
		{Title: "Нужен секрет", Assign: &engine.AssignCard{ID: a.ID, Kind: "secret", Secrets: []string{"school:pupil"}}},
	} {
		raw, _ := json.Marshal(card)
		if err := st.AddChatMsg(chat, "tool", string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	return chat, a
}

// Карточка поручения рисуется по базе: цель, шаги с состояниями, кнопки по
// состоянию; карточка секрета ведёт на добавление секрета с именем.
func TestAssignCardInChat(t *testing.T) {
	s, _ := testServer(t)
	chat, a := assignFixture(t, s)
	doc, _ := buildAll(t, s, "chat:"+strconv.FormatInt(chat, 10))
	var plan, secret *Component
	walkAll(doc.Components, func(c Component) {
		switch {
		case c.Type == "toolcard" && strings.HasPrefix(c.Title, "Поручение · "):
			plan = &c
		case c.Type == "toolcard" && c.Title == "Нужен секрет":
			secret = &c
		}
	})
	if plan == nil {
		t.Fatal("карточки поручения в чате нет")
	}
	if len(plan.Children) != 3 || plan.Children[0].Badges[0].Text != "сделано" || plan.Children[1].Badges[0].Text != "сейчас" {
		t.Fatalf("шаги на карточке: %+v", plan.Children)
	}
	if plan.Badges[0].Text != "идёт · 1 из 3" {
		t.Errorf("состояние: %+v", plan.Badges)
	}
	acts := map[string]Action{}
	for _, act := range everyAction(*plan) {
		acts[act.Action] = act
	}
	if _, ok := acts["assign_pause"]; !ok {
		t.Errorf("у идущего поручения нет «Остановить»: %+v", acts)
	}
	if c := acts["assign_cancel"]; c.Confirm == "" || c.ConfirmOK == "" {
		t.Errorf("«Отменить» без вопроса: %+v", c)
	}
	if secret == nil {
		t.Fatal("карточки «Нужен секрет» нет")
	}
	found := false
	for _, act := range everyAction(*secret) {
		if act.Action == "$goto" && act.Target == "settings:secrets@new~name:school:pupil" {
			found = true
		}
	}
	if !found {
		t.Errorf("карточка секрета не ведёт на добавление: %+v", everyAction(*secret))
	}

	// «Остановить» — и кнопка становится «Продолжить»
	if _, err := s.execute(actionRequest{Action: "assign_pause", Target: strconv.FormatInt(a.ID, 10)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.eng.Store().Assignment(a.ID); got.State != store.AssignPaused {
		t.Fatalf("после «Остановить»: %s", got.State)
	}
	if _, err := s.execute(actionRequest{Action: "assign_resume", Target: strconv.FormatInt(a.ID, 10)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute(actionRequest{Action: "assign_cancel", Target: strconv.FormatInt(a.ID, 10)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.eng.Store().Assignment(a.ID); got.State != store.AssignCancelled {
		t.Fatalf("после «Отменить»: %s", got.State)
	}
}

// На «Задачах» — раздел «Поручения» с ходом и переходом в чат; без
// поручений раздела нет.
func TestAssignSectionOnTasks(t *testing.T) {
	s, _ := testServer(t)
	doc, _ := buildAll(t, s, "tasks")
	for _, c := range doc.Components {
		if c.ID == "assignments" {
			t.Fatal("раздел поручений без поручений")
		}
	}
	chat, a := assignFixture(t, s)
	doc, _ = buildAll(t, s, "tasks")
	var sec *Component
	for i := range doc.Components {
		if doc.Components[i].ID == "assignments" {
			sec = &doc.Components[i]
		}
	}
	if sec == nil || len(sec.Children) != 1 {
		t.Fatalf("раздела «Поручения» нет: %+v", sec)
	}
	row := sec.Children[0]
	if row.Subtitle != a.Goal || !strings.Contains(row.Caption, "шаг 1 из 3") {
		t.Errorf("строка поручения: %q / %q", row.Subtitle, row.Caption)
	}
	goes := false
	for _, act := range everyAction(row) {
		if act.Action == "$goto" && act.Target == "chat:"+strconv.FormatInt(chat, 10) {
			goes = true
		}
	}
	if !goes {
		t.Errorf("строка не ведёт в чат поручения: %+v", everyAction(row))
	}
}
