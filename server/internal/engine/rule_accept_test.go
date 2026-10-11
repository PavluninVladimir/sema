package engine

import (
	"encoding/json"
	"testing"
)

// Кнопка «Записать в инструкцию» должна отвечать: карточка помечается
// записанной, а повтор не кладёт второй копии. Без этого человек жал её
// трижды, и правило трижды ложилось в инструкцию.
func TestRuleSavedOnce(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	e.ensureSelfModule()
	chat, err := st.CreateChat("проба")
	if err != nil {
		t.Fatal(err)
	}
	rule := "Управление VPN: устройства человека ищутся по comment в DHCP."
	if err := e.saveToolCard(chat, ToolCard{Title: "Запомнить правило · sema", Code: rule, Rule: selfModule}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := st.ChatMsgs(chat, 5)
	var msgID int64
	for _, m := range msgs {
		if m.Role == "tool" {
			msgID = m.ID
		}
	}
	if msgID == 0 {
		t.Fatal("карточка не сохранилась")
	}
	if err := e.AcceptRule(selfModule, msgID); err != nil {
		t.Fatal(err)
	}
	m, _ := e.Module(selfModule)
	if !ruleAlreadyIn(m.Instructions, rule) {
		t.Fatal("правило не попало в инструкцию")
	}
	// карточка помечена
	msg, _ := st.ChatMsg(msgID)
	var card ToolCard
	_ = json.Unmarshal([]byte(msg.Content), &card)
	if !card.RuleDone {
		t.Fatal("карточка не помечена записанной")
	}
	// второе нажатие ничего не добавляет
	if err := e.AcceptRule(selfModule, msgID); err != nil {
		t.Fatal(err)
	}
	m2, _ := e.Module(selfModule)
	if len(m2.Instructions) != len(m.Instructions) {
		t.Fatalf("повтор добавил копию: было %d, стало %d", len(m.Instructions), len(m2.Instructions))
	}
}
