package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Без доверенной модели закрытое не разбирается, и об этом сказано словами:
// письмо остаётся с причиной, «Здоровье» считает такие письма, модуль
// получает честный отказ с предложением назначить модель.
func TestWithoutTrustedModelClosedNotParsed(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := e.AddModel(ModelConf{Name: "облако", Type: "openai", BaseURL: "https://api.example.com/v1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	_ = e.SetRoleField(RoleFilter, "model", "облако")
	if err := st.SaveAccounts([]*store.Account{{Name: "работа", Email: "work@пример.рф", Host: "imap.пример.рф", Port: 993}}); err != nil {
		t.Fatal(err)
	}
	c := e.MailConf("работа")
	c.Purpose = store.PurposeWork
	if err := e.SaveMailConf("работа", c); err != nil {
		t.Fatal(err)
	}
	if !e.mailboxClosedForCloud("работа") {
		t.Fatal("рабочий ящик должен быть закрыт для внешней модели")
	}
	m := &store.Message{Account: "работа", FromAddr: "a@b", Subject: "тема", Snippet: "текст"}
	if p := e.filterModel(m); p != nil || m.Reason != reasonNoTrusted {
		t.Fatalf("закрытое письмо ушло бы внешней модели: %v %q", p, m.Reason)
	}
	// модуль: sema.llm({trusted: true}) — отказ с советом
	if _, err := e.toolLLM(t.Context(), "проба", map[string]any{"trusted": true}); err == nil || !strings.Contains(err.Error(), "доверенн") {
		t.Fatalf("модуль не получил честного отказа: %v", err)
	}
	// промпт чата называет ящик и говорит, что делать
	notice := closedNotice(map[string]bool{"работа": true})
	if !strings.Contains(notice, "работа") || !strings.Contains(notice, "доверенную модель") {
		t.Fatalf("промпт не называет ящик или не предлагает модель: %s", notice)
	}
	// «Здоровье» считает письма, ждущие модели
	m.Status = "inbox_ok"
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	cards, _ := e.HealthCards()
	found := false
	for _, c := range cards {
		if c.Key == "trust" && strings.Contains(c.Value, "1") {
			found = true
		}
	}
	if !found {
		t.Fatal("на «Здоровье» нет карточки «Ждут доверенной модели»")
	}
}
