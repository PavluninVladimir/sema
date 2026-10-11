package api

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// У безымянной учётки заголовком стоит номер, и первым знаком аватара
// выходил «+»: серый кружок с плюсом читался как кнопка «добавить»
// (14 сентября 2026). Аватар — только у названной учётки.
func TestAccountAvatarWithoutPlus(t *testing.T) {
	if а := accountAvatar(&store.TGAccount{Phone: "+70000000003"}); а != "" {
		t.Errorf("у безымянной учётки аватар %q — плюс читается как кнопка", а)
	}
	if а := accountAvatar(&store.TGAccount{Phone: "+70000000003", Title: "рабочая"}); а == "" || а == "+" {
		t.Errorf("у названной учётки аватар %q", а)
	}
	if accountAvatar(nil) != "" {
		t.Error("у пустой учётки аватар")
	}
}
