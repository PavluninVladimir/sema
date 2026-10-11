package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Запись, которая держит решение, нельзя закрыть «прочитано»: решение
// остаётся в очереди, и пинатель приносит запись обратно — «нажал
// выполнено, они вернулись» (13 сентября 2026).
func TestDecisionNotClosedAsRead(t *testing.T) {
	s, st := testServer(t)
	если := func(n *store.Notification) string {
		if _, err := st.PutNotification(n); err != nil {
			t.Fatal(err)
		}
		doc, err := s.buildScreen(notifyScreen, "", "", 50)
		if err != nil {
			t.Fatal(err)
		}
		return dumpScreen(doc)
	}
	// обычная запись: «Прочитано» есть
	dump := если(&store.Notification{Key: "прочее", Source: "core", Level: "info",
		Title: "Просто к сведению", Summary: "ничего делать не нужно"})
	if !strings.Contains(dump, "Прочитано") {
		t.Errorf("у записи без решения нет «Прочитано»:\n%s", dump)
	}
	// команда на подтверждении: «Прочитано» быть не должно
	dump = если(&store.Notification{Key: "ssh:проба", Source: "core", Level: "warn",
		Title: "сервер · id", Summary: "ждёт подтверждения",
		Actions: []store.NotifyAction{
			{Label: "Выполнить", Kind: "core", Op: "approve_ssh", Ref: "1", Style: "primary"},
			{Label: "Отклонить", Kind: "core", Op: "reject_ssh", Ref: "1"},
		}})
	часть := dump[strings.Index(dump, "сервер · id"):]
	if i := strings.Index(часть, "Просто к сведению"); i > 0 {
		часть = часть[:i]
	}
	if strings.Contains(часть, "Прочитано") || strings.Contains(часть, "Принято к сведению") {
		t.Errorf("у команды на подтверждении есть «Прочитано» — решение вернётся само:\n%s", часть)
	}
	if !strings.Contains(часть, "Выполнить") || !strings.Contains(часть, "Отклонить") {
		t.Errorf("у команды нет её решений:\n%s", часть)
	}
}
