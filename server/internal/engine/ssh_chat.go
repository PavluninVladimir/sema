package engine

// Команды на серверах из чата: модель просит блоком, Сёма выполняет чтение
// сразу и даёт модели второй ход по выводу; остальное ждёт кнопки человека.

import (
	"context"
	"fmt"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"log"
	"regexp"
	"strings"
)

// sshBlock — ```sema-ssh:prod-1\njournalctl -u nginx -n 50```
var sshBlock = regexp.MustCompile("(?s)```sema-ssh:([^\n]*)\n(.*?)```")

const maxSSHRuns = 3

// SSHCard — карточка команды в переписке.
type SSHCard struct {
	Host      string `json:"host"`
	Cmd       string `json:"cmd"`
	Status    string `json:"status"` // executed | pending | denied | norule | rejected | error
	Code      int    `json:"code,omitempty"`
	PendingID int64  `json:"pending_id,omitempty"`
	// NoRuleID — у сервера нет правила для этой команды: запрещена по
	// умолчанию, и у карточки две кнопки («Спрашивать для неё», «Разрешить её»)
	NoRuleID int64  `json:"norule_id,omitempty"`
	Why      string `json:"why,omitempty"`
}

// sshPrompt — договор с моделью: серверы по именам, их правила и что делать,
// когда правила нет. Правила у каждого сервера свои, поэтому в промпте они
// названы по серверам: «читающее выполняется сразу» было верно для всех
// сразу и неверно для каждого по отдельности.
func (e *Engine) sshPrompt() string {
	hosts, _ := e.store.Hosts()
	if len(hosts) == 0 {
		return ""
	}
	var rules []string
	for _, h := range hosts {
		list := e.store.HostRuleSet(h.Name)
		if list.Empty() {
			rules = append(rules, h.Name+": NO RULES — every command on it is refused until the user adds rules")
			continue
		}
		rules = append(rules, fmt.Sprintf("%s: runs at once (%d rules) e.g. %s …; asks the user first (%d); forbidden (%d)",
			h.Name, len(list.Allow), strings.Join(firstN(list.Allow, 10), ", "), len(list.Ask), len(list.Deny)))
	}
	return "\nServers available over SSH (by name, with OS): " + strings.Join(e.hostNamesWithKind(), ", ") + ". " +
		"RouterOS servers take RouterOS commands (/system resource print), not Linux ones. " +
		"To look at a server write a block ```sema-ssh:<server>\\n<one shell command>```. " +
		"Every server has its own access rules, three answers: allow, ask, deny. " +
		"Allowed commands run at once and you get the output. " +
		"A command the rules mark «ask» (restart, install, sudo…) is NOT run: Sema shows it to the user as a card with a confirm button — " +
		"so still write it as a sema-ssh block, say it waits for confirmation, and never ask the user to run it by hand. " +
		"A command no rule covers is refused by default: the user gets a card with buttons to add a rule — say so in one line, " +
		"do not retry it and do not look for another command that does the same. You can suggest a rule in words, but only the user's button adds it. " +
		"A denied command stays denied: never try to get around it. " +
		"Rules per server — " + strings.Join(rules, "; ") + ". " +
		"At most 3 commands per answer. " +
		"To wait and come back later (a process is still running, an update is in progress), write a block ```sema-timer:10 мин\\n<what to check>``` — " +
		"Sema shows a timer card and wakes this chat after that time with your note, then you check and continue.\n"
}

func firstN[T any](list []T, n int) []T {
	if len(list) > n {
		return list[:n]
	}
	return list
}

// runChatSSH выполняет блоки команд из ответа: карточки в переписку, а для
// выполненных — результаты, по которым модель ответит вторым ходом.
func (e *Engine) runChatSSH(ctx context.Context, chatID int64, answer string) (string, []ToolCard) {
	title := "чат"
	if c, err := e.store.Chat(chatID); err == nil && c.Title != "" {
		title = "чат «" + c.Title + "»"
	}
	return e.runSSHBlocks(ctx, chatID, answer, SSHOrigin{Who: title, ChatID: chatID})
}

// runSSHBlocks — то же для любого источника: модуль, пишущий в чат, получает
// карточки на свои серверы, а команда на подтверждение — кнопку человеку.
func (e *Engine) runSSHBlocks(ctx context.Context, chatID int64, answer string, o SSHOrigin) (string, []ToolCard) {
	blocks := sshBlock.FindAllStringSubmatch(answer, maxSSHRuns)
	if len(blocks) == 0 {
		return answer, nil
	}
	var executed []ToolCard
	for _, b := range blocks {
		host := e.resolveHost(b[1])
		cmd := strings.TrimSpace(b[2])
		if host == "" || cmd == "" {
			continue
		}
		out := e.RequestSSH(ctx, o, host, cmd)
		if out.PendingID > 0 {
			notePending(ctx) // ждёт кнопки: шаги этого ответа на этом кончаются
		}
		card, forModel := sshCardFor(host, cmd, out)
		if forModel {
			executed = append(executed, card)
		}
		_ = e.saveToolCard(chatID, card)
	}
	return strings.TrimSpace(sshBlock.ReplaceAllString(answer, "")), executed
}

// sshCardFor — карточка переписки по исходу запроса и признак «по ней
// модели нужен второй ход»: выполненное, отказанное и не вышедшее
// возвращается модели, а то, что ждёт человека, — нет (ответа по нему ещё
// нет).
func sshCardFor(host, cmd string, out SSHOutcome) (ToolCard, bool) {
	card := ToolCard{Title: host + " · " + cmd, SSH: &SSHCard{Host: host, Cmd: cmd, Why: out.Why}}
	switch {
	case out.PendingID > 0:
		card.SSH.Status, card.SSH.PendingID = "pending", out.PendingID
		card.Result = "требует подтверждения: " + out.Why
		return card, false
	case out.NoRuleID > 0:
		card.SSH.Status, card.SSH.NoRuleID = "norule", out.NoRuleID
		card.Err = out.Err
		return card, true
	case out.Err != "":
		card.SSH.Status, card.Err = "denied", out.Err
		if out.Policy == PolicyAllow {
			card.SSH.Status = "error"
		}
		return card, true
	}
	card.SSH.Status, card.SSH.Code = "executed", out.Code
	card.Code, card.Seconds = out.Output, out.Duration.Seconds()
	card.Result = fmt.Sprintf("код %d · %.1f с · %d символов", out.Code, out.Duration.Seconds(), len([]rune(out.Output)))
	return card, true
}

// resolveHost — в ограждении ```sema-ssh:<сервер>``` модель нередко дописывает
// пояснение («srv-bots · проверка процесса»): имя сервера ищется среди
// известных, а не берётся строкой целиком.
func (e *Engine) resolveHost(raw string) string {
	s := strings.TrimSpace(raw)
	hosts, _ := e.store.Hosts()
	for _, h := range hosts {
		if strings.EqualFold(h.Name, s) {
			return h.Name
		}
	}
	low := strings.ToLower(s)
	for _, h := range hosts {
		name := strings.ToLower(h.Name)
		if strings.HasPrefix(low, name) || strings.Contains(low, " "+name) || strings.Contains(low, "·"+name) {
			return h.Name
		}
	}
	if f := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '·' || r == ',' || r == ':' }); len(f) > 0 {
		return strings.TrimSpace(f[0])
	}
	return s
}

// sshResultsPrompt — вывод команд для второго хода модели.
func sshResultsPrompt(cards []ToolCard) string {
	var b strings.Builder
	b.WriteString("\nServer command results (real output; answer from it, do not invent):\n")
	for _, c := range cards {
		if c.Err != "" {
			fmt.Fprintf(&b, "- %s: ошибка — %s\n", c.Title, c.Err)
			continue
		}
		fmt.Fprintf(&b, "- %s (exit %d):\n%s\n", c.Title, c.SSH.Code, clip(c.Code, 6000))
	}
	b.WriteString("Give the final answer in Russian. If something needs changing on the server, propose the command in a sema-ssh block and say it will wait for confirmation.\n")
	return b.String()
}

// continueChat просит модель ответить по свежей карточке без новой реплики
// человека: так приходит ответ после подтверждённой команды.
func (e *Engine) continueChat(chatID int64) {
	chatID = e.chatForOutput(chatID) // Сёма продолжает в своём треде, не в ленте собеседника
	e.thinkingMu.Lock()
	if e.thinking == nil {
		e.thinking = map[int64]bool{}
	}
	if e.thinking[chatID] {
		e.thinkingMu.Unlock()
		return
	}
	e.thinking[chatID] = true
	e.thinkingMu.Unlock()
	e.Changed(store.ScreenChat(chatID))
	go func() {
		answer, err := e.chatAnswerStream(context.Background(), chatID, "")
		if err != nil {
			answer = "Не получилось ответить: " + err.Error()
		}
		answer = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(answer), "Sema:"))
		if answer != "" {
			if err := e.store.AddChatMsg(e.chatForOutput(chatID), "assistant", answer); err != nil {
				log.Printf("chat reply: %v", err)
			}
		}
		e.thinkingMu.Lock()
		delete(e.thinking, chatID)
		delete(e.partial, chatID)
		e.thinkingMu.Unlock()
		e.Changed(store.ScreenChat(chatID))
		e.flushCards(chatID) // карточки — после текста
	}()
}
