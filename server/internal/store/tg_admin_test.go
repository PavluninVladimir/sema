package store

import (
	"path/filepath"
	"testing"
)

func TestAdminRightOnTelegramChat(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const acc = "+70000000001"
	if err := st.SaveTGChat(&TGChat{Account: acc, ID: 7, Title: "Вадим", Kind: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTGChatRight(acc, 7, "admin", true); err != nil {
		t.Fatal(err)
	}
	c, ok := st.TGChat(acc, 7)
	if !ok || !c.Admin {
		t.Fatalf("право админа не сохранилось: %+v", c)
	}
	list, _ := st.TGChats(acc, false)
	if len(list) != 1 || !list[0].Admin {
		t.Fatal("в списке чатов права админа нет")
	}
	// право живёт в паре с учёткой: у чата с тем же номером в соседней
	// учётке его нет, и «Админ» там надо выдавать отдельно
	if err := st.SaveTGChat(&TGChat{Account: "+70000000002", ID: 7, Title: "Рабочий", Kind: "user"}); err != nil {
		t.Fatal(err)
	}
	if c, ok := st.TGChat("+70000000002", 7); !ok || c.Admin {
		t.Fatalf("право админа перетекло в соседнюю учётку: %+v", c)
	}
}
