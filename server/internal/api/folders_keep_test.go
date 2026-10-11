package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// «Только выключить» — неверно: папка может понадобиться. У замечания про
// зря разбираемые папки есть «Оставить», и оно не значит «молчи навсегда»:
// изменился состав папок — спрашиваем снова.
func TestKeepPointlessFolders(t *testing.T) {
	s, _ := testServer(t)
	const ящик = "почта@пример.рф"
	c := &store.MailConf{Folders: []string{"INBOX", "[Gmail]/Вся почта", "[Gmail]/Корзина"},
		Archive: "Archive", Junk: "Spam"}
	if err := s.eng.SaveMailConf(ящик, c); err != nil {
		t.Fatal(err)
	}
	замечание := func() string {
		var блоки []Component
		s.mailAttention(func(c Component) { блоки = append(блоки, c) }, ящик, s.eng.MailConf(ящик))
		return dumpScreen(&Document{Components: блоки})
	}
	dump := замечание()
	if !strings.Contains(dump, "где разбирать нечего") {
		t.Fatalf("замечания нет:\n%s", dump)
	}
	if !strings.Contains(dump, "Оставить") || !strings.Contains(dump, "Выключить все 2") {
		t.Errorf("у замечания нет обеих кнопок:\n%s", dump)
	}
	if _, err := s.execute(actionRequest{Action: "keep_folders", Target: ящик}); err != nil {
		t.Fatal(err)
	}
	if д := замечание(); strings.Contains(д, "где разбирать нечего") {
		t.Errorf("после «Оставить» замечание осталось:\n%s", д)
	}
	// папки остались в разборе: «оставить» ничего не выключает
	if len(pointlessFolders(s.eng.MailConf(ящик))) != 2 {
		t.Error("«Оставить» выключило папки")
	}
	// появилась новая зряшная папка — вопрос другой, спрашиваем сразу
	c = s.eng.MailConf(ящик)
	c.Folders = append(c.Folders, "[Gmail]/Черновики")
	if err := s.eng.SaveMailConf(ящик, c); err != nil {
		t.Fatal(err)
	}
	д := замечание()
	if !strings.Contains(д, "где разбирать нечего") {
		t.Errorf("новая папка не подняла замечание:\n%s", д)
	}
	if !strings.Contains(д, "Вы оставляли их") {
		t.Errorf("не сказано, что решение уже было:\n%s", д)
	}
}
