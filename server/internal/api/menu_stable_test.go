package api

import (
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Меню не перестраивается от одного нажатия вкладки. На «Отправителях»
// узел «Почта» остаётся раскрытым и выбранным, а счётчик у него — по всем
// ящикам, тот же, что на «Письмах»: 13 сентября 2026 он прыгал 16 → 48, а
// ящики сворачивались.
func TestMenuDoesNotJumpBetweenMessagesAndSenders(t *testing.T) {
	s, st := testServer(t)
	if err := st.SaveAccounts([]*store.Account{
		{Name: "проба", Email: "проба@пример.рф", Host: "imap.пример.рф", Port: 993},
		{Name: "второй", Email: "второй@пример.рф", Host: "imap.пример.рф", Port: 993},
	}); err != nil {
		t.Fatal(err)
	}
	узел := func(screen string) (web.Item, bool) {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		э := toScreen(doc)
		for _, п := range э.Items {
			if п.Name == "Почта" {
				return п, true
			}
		}
		return web.Item{}, false
	}
	письма, ок1 := узел("inbox@проба")
	отправители, ок2 := узел("senders")
	if !ок1 || !ок2 {
		t.Fatal("узла «Почта» нет в меню")
	}
	if !письма.Open || !отправители.Open {
		t.Errorf("узел «Почта» раскрыт на письмах=%v, на отправителях=%v — должен на обоих", письма.Open, отправители.Open)
	}
	// подсветка живёт на пункте ящика, а не на узле: на «Все ящики» белели
	// оба, на «Отправителях» — только узел, и заголовок «Почта» мигал
	if письма.Selected || отправители.Selected {
		t.Errorf("узел «Почта» сам подсвечен (письма=%v, отправители=%v) — подсветка должна быть на пункте ящика", письма.Selected, отправители.Selected)
	}
	выбранные := func(п web.Item) (names []string) {
		for _, д := range п.Children {
			if д.Selected {
				names = append(names, д.Addr)
			}
		}
		return
	}
	if в := выбранные(письма); len(в) != 1 || в[0] != "inbox@проба" {
		t.Errorf("на «inbox@проба» выбраны %v — ждали ровно свой ящик", в)
	}
	if в := выбранные(отправители); len(в) != 1 {
		t.Errorf("на «Отправителях» выбраны %v — ждали ровно один пункт", в)
	}
	if п, ок := узел("senders@проба"); ок {
		if в := выбранные(п); len(в) != 1 || в[0] != "inbox@проба" {
			t.Errorf("на «senders@проба» выбраны %v — ждали пункт того же ящика", в)
		}
	}
	if письма.Count != отправители.Count {
		t.Errorf("счётчик «Почты» прыгает: %q на письмах, %q на отправителях", письма.Count, отправители.Count)
	}
	if len(письма.Children) != len(отправители.Children) {
		t.Errorf("ящиков в узле %d и %d — меню перестраивается", len(письма.Children), len(отправители.Children))
	}
}
