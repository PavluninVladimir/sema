package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// «Не отвечает» и «отказывается отвечать» — разные беды: молчание пройдёт
// само, отказ — нет, и ждать его бессмысленно (14 сентября 2026,
// claude-fable-5-1 у роли «Арбитр»: finish_reason=refusal).
func TestDenialModelDiffersFromSilence(t *testing.T) {
	denials := []string{
		"claude-fable-5-1: failed: echolot: model refused the request: llm-api returned finish_reason=refusal",
		"model refused the request",
		"I can't help with that",
		"content_policy violation",
	}
	for _, s := range denials {
		if !modelDenial(errors.New(s)) {
			t.Errorf("отказ не узнан: %q", s)
		}
	}
	silence := []string{"context deadline exceeded", "connection refused", "503 service unavailable", "timeout"}
	for _, s := range silence {
		if modelDenial(errors.New(s)) {
			t.Errorf("молчание принято за отказ: %q", s)
		}
	}
}

// Запись про отказ говорит правду и чинится одним нажатием: запасная
// становится основной, прежняя основная уходит в запас.
func TestRecordAboutDenialAndShiftModel(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	roles := e.Roles()
	var role string
	for i := range roles {
		if roles[i].Name == RoleArbiter {
			roles[i].Model, roles[i].Spare = "отказчица", "запасная"
			role = roles[i].Name
		}
	}
	if role == "" {
		t.Skip("роли «Арбитр» нет в справочнике")
	}
	if err := e.SaveRoles(roles); err != nil {
		t.Fatal(err)
	}
	// три отказа подряд — запись про отказ, а не про молчание
	for i := 0; i < 3; i++ {
		e.countRoleFail(role, "отказчица", errors.New("model refused the request: finish_reason=refusal"))
	}
	rec, has := st.OpenNotificationByKey("role:" + role)
	if !has {
		t.Fatal("записи про роль нет")
	}
	if !strings.Contains(rec.Title, "отказывается") {
		t.Errorf("заголовок говорит про молчание: %q", rec.Title)
	}
	var hasButton bool
	for _, a := range rec.Actions {
		if a.Op == "role_use_spare" && a.Ref == role {
			hasButton = true
		}
	}
	if !hasButton {
		t.Fatalf("нет кнопки «поставить запасную»: %+v", rec.Actions)
	}
	// нажали — модели поменялись местами, отметка снята, запись закрыта
	if err := e.MakeSpareMain(role); err != nil {
		t.Fatal(err)
	}
	for _, r := range e.Roles() {
		if r.Name != role {
			continue
		}
		if r.Model != "запасная" || r.Spare != "отказчица" {
			t.Errorf("модели не поменялись: основная %q, запасная %q", r.Model, r.Spare)
		}
		if r.LastErr != "" {
			t.Errorf("отметка о сбое осталась: %q", r.LastErr)
		}
	}
	// молчание по-прежнему читается как молчание
	e.clearRoleFails(role)
	for i := 0; i < 3; i++ {
		e.countRoleFail(role, "запасная", errors.New("context deadline exceeded"))
	}
	if rec, has := st.OpenNotificationByKey("role:" + role); has && strings.Contains(rec.Title, "отказывается") {
		t.Errorf("таймаут назван отказом: %q", rec.Title)
	}
	_ = store.NotifyDone
}
