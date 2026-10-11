package engine

// Команда без правила. Не подошло ни одно правило сервера — команда
// запрещена по умолчанию, но не молча: человеку приходит карточка «на
// сервере X нет правила для этой команды» с двумя кнопками. «Спрашивать для
// неё» заводит правило «Спросить», и дальше команда идёт обычным
// подтверждением («Выполнить»). «Разрешить её» заводит правило «Разрешить»
// — только если команду вообще можно разрешить: с sudo или неясным
// синтаксисом она проходит лишь по «Спросить».
//
// Сёма правил сам не заводит. Модель может предложить правило словами, но
// завести его может только кнопка человека: запрос лежит отдельно от
// команд на подтверждении (`ssh_norule`), чтобы «Выполнить» не дотянулась до
// того, что никто не разрешал даже спрашивать. Команда, запрещённая
// правилом «Запретить», кнопок не имеет: запрет человек ставил сам.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// noRuleNotifyKey — один и тот же запрос на правило заменяет прежний, а не
// плодит новые: задача просит свою команду каждый прогон.
func noRuleNotifyKey(host, cmd string) string {
	sum := sha256.Sum256([]byte(host + "\n" + strings.TrimSpace(cmd)))
	return "rule:" + host + ":" + hex.EncodeToString(sum[:6])
}

// recordNoRule — записать команду без правила и позвать человека. Ночная
// смена (`Quiet`) зовёт не по каждой команде: она перечисляет их в своём
// отчёте, а запрос остаётся ждать решения.
func (e *Engine) recordNoRule(o SSHOrigin, h *store.Host, cmd string, r Ruling) int64 {
	n := &store.SSHNoRule{Host: h.Name, Cmd: cmd, Who: o.Who, ChatID: o.ChatID, Why: r.Why, CanAllow: r.CanAllow()}
	id, fresh, err := e.store.AddSSHNoRule(n)
	if err != nil {
		return 0
	}
	if o.Quiet {
		return id
	}
	if fresh {
		e.notifyNoRule(id, n)
	} else if _, open := e.store.OpenNotificationByKey(noRuleNotifyKey(h.Name, cmd)); !open {
		e.notifyNoRule(id, n) // потерянная кнопка: у ждущего запроса всегда есть уведомление
	}
	return id
}

// notifyNoRule — «на сервере нет правила для этой команды».
func (e *Engine) notifyNoRule(id int64, n *store.SSHNoRule) {
	ref := strconv.FormatInt(id, 10)
	actions := []store.NotifyAction{
		{Label: "Спрашивать для неё", Kind: "core", Op: "rule_ask", Ref: ref, Style: "primary"},
	}
	if n.CanAllow {
		actions = append(actions, store.NotifyAction{Label: "Разрешить её", Kind: "core", Op: "rule_allow", Ref: ref})
	}
	actions = append(actions,
		store.NotifyAction{Label: "Правила сервера", Kind: "goto", Screen: ServerScreen(n.Host, "rules")},
		store.NotifyAction{Label: "Оставить запрещённой", Kind: "core", Op: "rule_dismiss", Ref: ref})
	fields := []store.NotifyField{
		{Label: "Команда", Value: n.Cmd}, {Label: "Сервер", Value: n.Host}, {Label: "Кто просил", Value: n.Who},
		{Label: "Почему запрещена", Value: n.Why, Level: "warn"},
	}
	if !n.CanAllow {
		fields = append(fields, store.NotifyField{Label: "Разрешить нельзя",
			Value: "sudo, перенаправление в файл и неясный синтаксис проходят только по правилу «Спросить»"})
	}
	_, _ = e.Notify(&store.Notification{
		Key: noRuleNotifyKey(n.Host, n.Cmd), Source: "core", Level: "warn",
		Title:   n.Host + " · нет правила: " + clip(n.Cmd, 50),
		Summary: "на сервере «" + n.Host + "» нет правила для этой команды — она не выполнена",
		Fields:  fields, Actions: actions,
		Ref: "norule:" + ref,
	})
}

// DecideNoRule — решение человека по команде без правила: завести «Спросить»
// (`store.NoRuleAsk`), завести «Разрешить» (`store.NoRuleAllow`) или оставить
// запрещённой (`store.NoRuleDismissed`). После правила команда идёт тем же
// путём, что и любая: «Спросить» — карточка «Выполнить», «Разрешить» —
// выполнение сразу. Результат возвращается туда, откуда пришла команда: в
// чат карточкой, а без чата — в само уведомление.
func (e *Engine) DecideNoRule(ctx context.Context, id int64, answer, by string) error {
	replay, err := e.applyNoRule(ctx, id, answer, by)
	if err != nil {
		return err
	}
	replay()
	return nil
}

// DecideNoRuleAsync — то же, но после записи правила команда идёт своим путём
// в фоне: она может идти долго (суд двух моделей, выполнение на сервере), а
// нажатие кнопки не должно ждать. Правило и ошибки решения — сразу.
func (e *Engine) DecideNoRuleAsync(ctx context.Context, id int64, answer, by string) error {
	replay, err := e.applyNoRule(ctx, id, answer, by)
	if err != nil {
		return err
	}
	go replay()
	return nil
}

// applyNoRule — решение записано (правило заведено, запрос закрыт, уведомление
// снято); возвращает то, что осталось сделать: пустить команду своим путём.
func (e *Engine) applyNoRule(ctx context.Context, id int64, answer, by string) (func(), error) {
	n, ok := e.store.SSHNoRuleByID(id)
	if !ok {
		return nil, fmt.Errorf("запроса на правило %d нет", id)
	}
	if n.Decided != "" {
		return nil, fmt.Errorf("этот запрос уже решён: %s", n.Decided)
	}
	key := noRuleNotifyKey(n.Host, n.Cmd)
	if _, has := e.store.Host(n.Host); !has {
		return nil, fmt.Errorf("сервера %q больше нет", n.Host)
	}
	card := ToolCard{Title: n.Host + " · " + n.Cmd, SSH: &SSHCard{Host: n.Host, Cmd: n.Cmd, NoRuleID: id}}
	match := func(c ToolCard) bool { return c.SSH != nil && c.SSH.NoRuleID == id }
	switch answer {
	case store.NoRuleDismissed:
		if err := e.store.DecideSSHNoRule(id, answer); err != nil {
			return nil, err
		}
		e.CloseNotification(key, store.NotifyDismissed, "оставлена запрещённой")
		card.SSH.Status, card.Result = "rejected", "оставлена запрещённой: правила не заведено"
		e.putNoRuleCard(n.ChatID, match, card)
		return func() {}, nil
	case store.NoRuleAsk:
		if err := e.store.AddHostRule(n.Host, store.RuleAsk, n.Cmd); err != nil {
			return nil, err
		}
	case store.NoRuleAllow:
		// проверка заново, а не по записи: «можно разрешить» решает разбор
		// команды, и карточка, собранная старой сборкой, его не обойдёт
		if r := e.Decide(n.Host, n.Cmd); !r.CanAllow() {
			return nil, fmt.Errorf("такую команду можно только спрашивать, а не разрешать (%s): нажмите «Спрашивать для неё»", firstNonEmpty(r.Unsafe, r.Why))
		}
		if err := e.store.AddHostRule(n.Host, store.RuleAllow, n.Cmd); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("решение бывает «спрашивать», «разрешить» или «оставить запрещённой»")
	}
	if err := e.store.DecideSSHNoRule(id, answer); err != nil {
		return nil, err
	}
	e.store.LogSSH(store.SSHLogEntry{Host: n.Host, Cmd: n.Cmd, Who: n.Who, Decision: store.SSHRuled,
		Output: "человек завёл правило «" + PolicyTitle(answer) + "» для этой команды (" + by + ")"})
	e.CloseNotification(key, store.NotifyDone, "правило заведено")

	// теперь правило есть: команда идёт своим путём
	return func() {
		o := SSHOrigin{Who: n.Who, ChatID: n.ChatID}
		started := e.RequestSSH(ctx, o, n.Host, n.Cmd)
		next, _ := sshCardFor(n.Host, n.Cmd, started)
		e.putNoRuleCard(n.ChatID, match, next)
		if n.ChatID == 0 && started.PendingID == 0 {
			// выполнено сразу (или не вышло): итог без чата ложится в уведомление
			level, value := "ok", clip(started.Output, 1500)
			if started.Err != "" {
				level, value = "alert", clip(started.Err, 500)
			}
			_ = e.setNotificationResultByKey(key, store.NotifyField{Label: "Результат", Value: value, Level: level})
		}
		if n.ChatID > 0 && started.PendingID == 0 {
			e.continueChat(n.ChatID) // Сёма отвечает по результату, как после подтверждённой команды
		}
	}, nil
}

// putNoRuleCard — карточка «нет правила» становится карточкой того, что
// вышло, на месте; карточки нет — пишется новая.
func (e *Engine) putNoRuleCard(chatID int64, match func(ToolCard) bool, card ToolCard) {
	if chatID <= 0 {
		return
	}
	if !e.replaceCardWhere(chatID, match, card) {
		_ = e.saveToolCard(chatID, card)
	}
}
