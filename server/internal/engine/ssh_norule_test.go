package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Команда без правила и команда, запрещённая правилом, — разные вещи. У первой
// в чате карточка «нет правила» с двумя кнопками, у второй кнопок нет вовсе.

// ruleChat — сервер «проба» с набором Linux, подменённым транспортом и чат, в
// котором модель просит команды.
func ruleChat(t *testing.T) (*Engine, *store.Store, *nightSSH, int64) {
	t.Helper()
	e, st := newTestEngine(t, "", nil)
	addHost(t, st, "проба", store.HostLinux)
	takeSet(t, e, "проба", store.RuleSetLinux)
	if err := st.SaveSecret(&store.Secret{Name: "ssh:проба", Kind: "ssh"}, "ключ-понарошку"); err != nil {
		t.Fatal(err)
	}
	fake := &nightSSH{}
	e.sshVia = fake.run
	chatID, err := st.CreateChat("Серверы")
	if err != nil {
		t.Fatal(err)
	}
	return e, st, fake, chatID
}

// lastSSHCard — последняя карточка команды в переписке.
func lastSSHCard(t *testing.T, st *store.Store, chatID int64) ToolCard {
	t.Helper()
	msgs, err := st.ChatMsgs(chatID, 50)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if card, ok := ParseToolCard(msgs[i].Content); ok && card.SSH != nil {
			return card
		}
	}
	t.Fatal("в переписке нет карточки команды")
	return ToolCard{}
}

func sshAsk(host, cmd string) string { return "```sema-ssh:" + host + "\n" + cmd + "\n```" }

// Команда без правила: запрещена по умолчанию, не выполнена, человеку —
// карточка и уведомление с двумя кнопками. После «Спрашивать для неё» это
// обычное подтверждение, а не выполнение и не отказ.
func TestChatCommandWithoutRuleGetsTwoButtons(t *testing.T) {
	e, st, fake, chatID := ruleChat(t)
	_, cards := e.runSSHBlocks(context.Background(), chatID, sshAsk("проба", "docker restart web"),
		SSHOrigin{Who: "чат «Серверы»", ChatID: chatID})
	if len(cards) != 1 || cards[0].SSH.Status != "norule" || cards[0].SSH.NoRuleID == 0 {
		t.Fatalf("карточка команды без правила: %+v", cards)
	}
	if !strings.Contains(cards[0].Err, "нет правила") {
		t.Errorf("модель не узнала причину отказа: %q", cards[0].Err)
	}
	if fake.index("docker restart web") >= 0 {
		t.Fatal("команда без правила ушла на сервер")
	}
	if pending, _ := st.SSHPendingList(); len(pending) != 0 {
		t.Fatalf("у команды без правила есть кнопка «Выполнить»: %+v", pending)
	}
	// уведомление с двумя кнопками-решениями
	n, ok := st.OpenNotificationByKey(noRuleNotifyKey("проба", "docker restart web"))
	if !ok {
		t.Fatal("человек не получил уведомление «нет правила»")
	}
	ops := map[string]bool{}
	for _, a := range n.Actions {
		ops[a.Op] = true
	}
	if !ops["rule_ask"] || !ops["rule_allow"] {
		t.Errorf("в уведомлении нет обеих кнопок: %+v", n.Actions)
	}

	// «Спрашивать для неё»: правило «Спросить» у ЭТОГО сервера, команда ждёт кнопки
	if err := e.DecideNoRule(context.Background(), cards[0].SSH.NoRuleID, store.NoRuleAsk, "тест"); err != nil {
		t.Fatal(err)
	}
	if list := st.HostRuleSet("проба"); !hasPattern(list.Ask, "docker restart web") {
		t.Fatalf("правило «Спросить» не заведено: %+v", list.Ask)
	}
	pending, _ := st.SSHPendingList()
	if len(pending) != 1 || pending[0].Cmd != "docker restart web" {
		t.Fatalf("после правила команда не стала обычным подтверждением: %+v", pending)
	}
	if card := lastSSHCard(t, st, chatID); card.SSH.Status != "pending" || card.SSH.PendingID != pending[0].ID {
		t.Fatalf("карточка «нет правила» не стала карточкой подтверждения: %+v", card.SSH)
	}
	if fake.index("docker restart web") >= 0 {
		t.Fatal("«Спрашивать» выполнило команду без кнопки")
	}
	// запрос решён: повторно его не решить
	if err := e.DecideNoRule(context.Background(), cards[0].SSH.NoRuleID, store.NoRuleAllow, "тест"); err == nil {
		t.Fatal("решённый запрос решился второй раз")
	}
}

// «Разрешить её»: правило «Разрешить» этому серверу, и команда сразу выполняется.
func TestNoRuleAllowRunsCommand(t *testing.T) {
	e, st, fake, chatID := ruleChat(t)
	_, cards := e.runSSHBlocks(context.Background(), chatID, sshAsk("проба", "docker restart web"),
		SSHOrigin{Who: "чат «Серверы»", ChatID: chatID})
	if len(cards) != 1 {
		t.Fatalf("карточки: %+v", cards)
	}
	if err := e.DecideNoRule(context.Background(), cards[0].SSH.NoRuleID, store.NoRuleAllow, "тест"); err != nil {
		t.Fatal(err)
	}
	if !hasPattern(st.HostRuleSet("проба").Allow, "docker restart web") {
		t.Fatal("правило «Разрешить» не заведено")
	}
	if fake.index("docker restart web") < 0 {
		t.Fatalf("разрешённая команда не выполнена: %v", fake.list())
	}
	if card := lastSSHCard(t, st, chatID); card.SSH.Status != "executed" {
		t.Fatalf("карточка не стала выполненной: %+v", card.SSH)
	}
}

// С sudo «Разрешить её» не предлагается и не принимается: sudo проходит только
// по «Спросить». Кнопка «Спрашивать для неё» при этом остаётся.
func TestNoRuleSudoCanOnlyBeAsked(t *testing.T) {
	e, st, _, chatID := ruleChat(t)
	_, cards := e.runSSHBlocks(context.Background(), chatID, sshAsk("проба", "sudo docker restart web"),
		SSHOrigin{Who: "чат «Серверы»", ChatID: chatID})
	if len(cards) != 1 || cards[0].SSH.Status != "norule" {
		t.Fatalf("sudo без правила: %+v", cards)
	}
	n, _ := st.SSHNoRuleByID(cards[0].SSH.NoRuleID)
	if n == nil || n.CanAllow {
		t.Fatalf("sudo можно разрешить: %+v", n)
	}
	note, _ := st.OpenNotificationByKey(noRuleNotifyKey("проба", "sudo docker restart web"))
	for _, a := range note.Actions {
		if a.Op == "rule_allow" {
			t.Fatal("в уведомлении про sudo есть «Разрешить её»")
		}
	}
	if err := e.DecideNoRule(context.Background(), n.ID, store.NoRuleAllow, "тест"); err == nil {
		t.Fatal("«Разрешить» принято для sudo")
	}
	if list := st.HostRuleSet("проба"); hasPattern(list.Allow, "sudo docker restart web") {
		t.Fatal("правило «Разрешить» на sudo заведено")
	}
	if err := e.DecideNoRule(context.Background(), n.ID, store.NoRuleAsk, "тест"); err != nil {
		t.Fatalf("«Спрашивать» для sudo: %v", err)
	}
}

// Запрет по правилу «Запретить» кнопок не имеет: ни запроса на правило, ни
// уведомления, ни карточки подтверждения.
func TestDenyRuleHasNoButtons(t *testing.T) {
	e, st, fake, chatID := ruleChat(t)
	_, cards := e.runSSHBlocks(context.Background(), chatID, sshAsk("проба", "rm -rf /tmp/x"),
		SSHOrigin{Who: "чат «Серверы»", ChatID: chatID})
	if len(cards) != 1 || cards[0].SSH.Status != "denied" || cards[0].SSH.NoRuleID != 0 {
		t.Fatalf("запрет по правилу: %+v", cards)
	}
	if list := st.SSHNoRuleList(10); len(list) != 0 {
		t.Fatalf("у запрета по правилу есть запрос на правило: %+v", list)
	}
	if pending, _ := st.SSHPendingList(); len(pending) != 0 {
		t.Fatalf("у запрета по правилу есть кнопка «Выполнить»: %+v", pending)
	}
	if fake.index("rm -rf /tmp/x") >= 0 {
		t.Fatal("запрещённая команда ушла на сервер")
	}
}

// «Оставить запрещённой» закрывает запрос без правила: команда остаётся
// запрещённой, а карточка говорит об этом.
func TestNoRuleDismissKeepsDenied(t *testing.T) {
	e, st, _, chatID := ruleChat(t)
	_, cards := e.runSSHBlocks(context.Background(), chatID, sshAsk("проба", "docker restart web"),
		SSHOrigin{Who: "чат «Серверы»", ChatID: chatID})
	if err := e.DecideNoRule(context.Background(), cards[0].SSH.NoRuleID, store.NoRuleDismissed, "тест"); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.Classify("проба", "docker restart web"); got != PolicyDeny {
		t.Fatalf("после «Оставить запрещённой» команда получила %q", got)
	}
	if list := st.HostRuleSet("проба"); hasPattern(list.Ask, "docker restart web") || hasPattern(list.Allow, "docker restart web") {
		t.Fatal("правило заведено, хотя человек оставил команду запрещённой")
	}
	if card := lastSSHCard(t, st, chatID); card.SSH.Status != "rejected" {
		t.Fatalf("карточка: %+v", card.SSH)
	}
}

// «Выполнить и впредь без вопроса» пишет правило «Разрешить» ЭТОМУ серверу, а
// не всем: у каждого сервера свои правила. Команду с sudo так не разрешить.
func TestApproveAndAllowWritesRuleToThisHostOnly(t *testing.T) {
	e, st, _, _ := ruleChat(t)
	addHost(t, st, "соседний", store.HostLinux)
	addRules(t, st, "проба", store.RuleAsk, "docker restart")
	addRules(t, st, "соседний", store.RuleAsk, "docker restart")
	out := e.RequestSSH(context.Background(), SSHOrigin{Who: "тест"}, "проба", "docker restart web")
	if out.PendingID == 0 {
		t.Fatalf("команда не ждёт кнопки: %+v", out)
	}
	if err := e.ApproveSSH(context.Background(), out.PendingID, true); err != nil {
		t.Fatal(err)
	}
	if !hasPattern(st.HostRuleSet("проба").Allow, "docker restart web") {
		t.Fatal("«впредь без вопроса» не записало правило серверу")
	}
	if hasPattern(st.HostRuleSet("соседний").Allow, "docker restart web") {
		t.Fatal("правило попало не только этому серверу")
	}
	if got, _ := e.Classify("проба", "docker restart web"); got != PolicyAllow {
		t.Errorf("после «впредь без вопроса» команда получила %q", got)
	}
	if got, _ := e.Classify("соседний", "docker restart web"); got != PolicyAsk {
		t.Errorf("у соседнего сервера ответ изменился: %q", got)
	}
	// sudo этой кнопкой не разрешается
	addRules(t, st, "проба", store.RuleAsk, "docker rm")
	out = e.RequestSSH(context.Background(), SSHOrigin{Who: "тест"}, "проба", "sudo docker rm web")
	if out.PendingID == 0 {
		t.Fatalf("sudo при правиле «Спросить» не ждёт кнопки: %+v", out)
	}
	if err := e.ApproveSSH(context.Background(), out.PendingID, true); err != nil {
		t.Fatal(err)
	}
	if hasPattern(st.HostRuleSet("проба").Allow, "sudo docker rm web") {
		t.Fatal("sudo стал правилом «Разрешить»")
	}
}

// Кнопка не сильнее запрета: команда ждала, человек тем временем вписал
// «Запретить» — «Выполнить» отказывает.
func TestApproveCannotBeatDenyAddedLater(t *testing.T) {
	e, st, fake, _ := ruleChat(t)
	addRules(t, st, "проба", store.RuleAsk, "docker restart")
	out := e.RequestSSH(context.Background(), SSHOrigin{Who: "тест"}, "проба", "docker restart web")
	if out.PendingID == 0 {
		t.Fatalf("команда не ждёт кнопки: %+v", out)
	}
	addRules(t, st, "проба", store.RuleDeny, "docker restart")
	if err := e.ApproveSSH(context.Background(), out.PendingID, false); err == nil {
		t.Fatal("«Выполнить» сильнее «Запретить»")
	}
	if fake.index("docker restart web") >= 0 {
		t.Fatal("запрещённая команда ушла на сервер по кнопке")
	}
}

// hasPattern — образец есть в списке.
func hasPattern(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// Команда, ждавшая кнопки с прежних времён (без правила она шла на
// подтверждение), после перехода на правила серверов не выполняется по старой
// кнопке и не пропадает: она становится запросом на правило с двумя кнопками.
func TestOldPendingWithoutRuleBecomesRuleRequest(t *testing.T) {
	e, st, fake, chatID := ruleChat(t)
	id, err := st.AddSSHPending(&store.SSHPending{Host: "проба", Cmd: "sudo systemctl restart nginx", Who: "чат «Серверы»", ChatID: chatID})
	if err != nil {
		t.Fatal(err)
	}
	err = e.ApproveSSH(context.Background(), id, false)
	if err == nil || !strings.Contains(err.Error(), "нет правила") {
		t.Fatalf("старая кнопка не сказала про правило: %v", err)
	}
	if fake.index("sudo systemctl restart nginx") >= 0 {
		t.Fatal("команда без правила ушла на сервер по старой кнопке")
	}
	if pending, _ := st.SSHPendingList(); len(pending) != 0 {
		t.Fatalf("команда осталась ждать «Выполнить»: %+v", pending)
	}
	list := st.SSHNoRuleList(10)
	if len(list) != 1 || list[0].Cmd != "sudo systemctl restart nginx" || list[0].CanAllow {
		t.Fatalf("запрос на правило: %+v", list)
	}
}
