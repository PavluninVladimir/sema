package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Раздел, чьей части в реестре нет, роняет тест, а не экран у человека.
// Обход тот же, что у теста на сборку экранов: все экраны ядра целиком.
func TestEveryLazySectionHasFragment(t *testing.T) {
	s, st := testServer(t)
	// чаты нужны, иначе разделы не строятся вовсе
	if err := st.SaveMAXAccount(&store.MAXAccount{Phone: "+70000000000", State: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMAXChat(&store.MAXChat{ID: 1, Title: "Проба", Kind: "dialog"}); err != nil {
		t.Fatal(err)
	}
	// без ключей Telegram показывает мастер, а не вкладки
	if err := s.eng.SaveTGCreds("12345", "хэш"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTGAccount(&store.TGAccount{Phone: telegramAccount, State: "ok", Enabled: true, Every: 1, Batch: 50}); err != nil {
		t.Fatal(err)
	}
	// чат принадлежит учётке: без её номера он ничей и в список не попадёт
	if err := st.SaveTGChat(&store.TGChat{Account: telegramAccount, ID: 1, Title: "Проба", Kind: "user"}); err != nil {
		t.Fatal(err)
	}
	// канал — ради ленивого раздела «Список каналов»
	if err := st.SaveTGChat(&store.TGChat{Account: telegramAccount, ID: 2, Title: "Канал", Kind: "channel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveMemory(&store.Memory{Kind: store.MemFact, Area: "личное",
		Text: "ждёт решения", Status: store.MemPending}); err != nil {
		t.Fatal(err)
	}
	// выключенная встроенная задача — ради ленивого раздела «Выключены»
	if err := st.CreateTask(&store.Task{Title: "Выключенная проба", System: "проба-выкл",
		Schedule: "каждый час", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	части := s.fragments()
	seen := map[string]bool{}
	screens := append(append([]string{}, coreScreens...), allScreens...)
	screens = append(screens, tgScreen+"~"+tgTabChats)
	for _, screen := range screens {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		for _, ч := range lazySections(doc.Components) {
			seen[ч] = true
			if _, has := части[ч]; !has {
				t.Errorf("экран %s просит часть %q, а такой в реестре нет", screen, ч)
			}
		}
	}
	for ч := range части {
		if !seen[ч] {
			t.Errorf("часть %q есть в реестре, но её не просит ни один экран", ч)
		}
	}
	// у ленивого раздела детей быть не должно — иначе он не ленивый
	doc, _ := s.buildScreen(maxScreen, "", "", 0)
	for _, c := range doc.Components {
		if c.Lazy != "" && len(c.Children) > 0 {
			t.Errorf("раздел %q ленивый, а дети приехали сразу: %d", c.Lazy, len(c.Children))
		}
	}
}

// Часть отдаёт первые двадцать, выбранные сверху, остальное — по «ещё»;
// поиск идёт на сервере по имени и виду, а не среди загруженного.
func TestMAXChatsFragmentLimitAndSearch(t *testing.T) {
	s, st := testServer(t)
	for i := int64(1); i <= 30; i++ {
		c := &store.MAXChat{ID: i, Title: "Чат " + strings.Repeat("я", int(i)), Kind: "chat"}
		if i == 30 {
			c.Collect, c.Title, c.Kind = true, "Ozon доставка", "channel"
		}
		if err := st.SaveMAXChat(c); err != nil {
			t.Fatal(err)
		}
	}
	// чаты — строки матрицы прав, а «ещё» — кнопка рядом с ней
	chatRows := func(rows []Component) (chats []Component, ещё string) {
		for _, r := range rows {
			switch {
			case r.Type == "matrix":
				chats = append(chats, r.Children...)
			case r.Type == "button" && len(r.Actions) > 0 && r.Actions[0].Action == "$more":
				ещё = r.Actions[0].Label
			}
		}
		return chats, ещё
	}
	rows, err := s.buildPart(maxScreen, "max-chats", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	чаты, ещё := chatRows(rows)
	if len(чаты) != fragmentLimit {
		t.Errorf("в первой части %d чатов, а должно быть %d", len(чаты), fragmentLimit)
	}
	if !strings.Contains(ещё, "20 из 30") {
		t.Errorf("кнопка «ещё» говорит %q", ещё)
	}
	if чаты[0].Subtitle != "Ozon доставка" {
		t.Errorf("выбранный чат не сверху: первым стоит %q", чаты[0].Subtitle)
	}
	// у каждой строки четыре клетки прав, и каждая зовёт прежнее действие
	for i, col := range []string{"collect", "files", "send", "blocked"} {
		cell := чаты[0].Cells[i]
		if cell.Action.Action != "max_chat_right" || !strings.HasSuffix(cell.Action.Target, "~"+col) {
			t.Errorf("клетка %d: %+v, ждали max_chat_right ~%s", i, cell.Action, col)
		}
	}
	// удвоенный предел — как нажатие «ещё»
	rows, _ = s.buildPart(maxScreen, "max-chats", "", 40)
	if чаты, ещё = chatRows(rows); len(чаты) != 30 || ещё != "" {
		t.Errorf("после «ещё» строк %d (кнопка %q), ждали 30 без кнопки", len(чаты), ещё)
	}
	// поиск по виду словом и по имени
	for req, want := range map[string]string{"канал": "Ozon доставка", "ozon": "Ozon доставка", "канал ozon": "Ozon доставка"} {
		rows, _ = s.buildPart(maxScreen, "max-chats", req, 0)
		if чаты, _ = chatRows(rows); len(чаты) != 1 || чаты[0].Subtitle != want {
			t.Errorf("поиск %q: нашлось %d", req, len(чаты))
		}
	}
	rows, _ = s.buildPart(maxScreen, "max-chats", "такого-нет", 0)
	if len(rows) != 1 || !strings.Contains(rows[0].Subtitle, "не нашлось") {
		t.Errorf("пустой поиск не объясняет себя: %+v", rows)
	}
}

// Неизвестная часть — отказ, а не пустой ответ: молчание здесь выглядело
// бы как «в разделе пусто».
func TestUnknownFragmentDenied(t *testing.T) {
	s, _ := testServer(t)
	if _, err := s.buildPart(maxScreen, "такой-нет", "", 0); err == nil {
		t.Fatal("неизвестная часть собралась пустой вместо отказа")
	}
}
