package engine

import (
	"errors"
	"testing"
)

// Отметку о сбое никто не снимал: «Стоит посмотреть» показывало вчерашний
// отказ модели, которая с тех пор весь день отвечает. Замечание, которое
// не гаснет, перестают читать целиком — вместе с теми, что важны.
func TestSuccessClearsMarkAboutFailure(t *testing.T) {
	fake := &fakeProvider{answer: "готово"}
	e, _ := newTestEngine(t, "рабочая", fake)
	assignRole(t, e, RoleChat, "рабочая")

	// сбой записан
	e.noteRoleFail(RoleChat, "рабочая", errProbe)
	if e.Role(RoleChat).LastErr == "" {
		t.Fatal("отметка о сбое не записалась")
	}
	// а теперь модель ответила
	if _, _, err := e.AskRole(t.Context(), RoleChat, "проба"); err != nil {
		t.Fatal(err)
	}
	if got := e.Role(RoleChat).LastErr; got != "" {
		t.Errorf("после успешного ответа отметка осталась: %q", got)
	}
	if e.Role(RoleChat).Last == "" {
		t.Error("успех не записан")
	}
}

// errProbe — любая ошибка: важно, что отметка появилась, а не какая она.
var errProbe = errors.New("нет связи с сетью")
