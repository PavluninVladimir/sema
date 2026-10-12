package engine

// Обрыв связи с агентом — событие, а не тишина. Соединение постоянное,
// поэтому обрыв известен точно. Но человеку важна не каждая секунда
// разрыва, а пропажа: шлюз переподключается за секунду, а уведомление
// уходило пушем на телефон на каждый такой обрыв — 12 октября 2026 это
// читалось как «Сёма отваливается постоянно», хотя связи не было 0 с.
// Поэтому уведомление ждёт `agents.off_notify_s` (две минуты): вернулся
// агент раньше — человека не будили. 0 — сразу, как раньше. Закрывается
// уведомление само, когда агент вернулся.
//
// Уровень «внимание», а не тревога: мак спит по ночам, и ночной обрыв —
// норма жизни, а не поломка. Кому агент сейчас не нужен, тот выключает
// его на экране «Агенты» — у выключенного обрыв молчит.

import (
	"fmt"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// AgentOffNotifyKey — сколько секунд агент может быть без связи, прежде чем
// об этом скажут человеку.
const AgentOffNotifyKey = "agents.off_notify_s"

// agentOffKey — ключ уведомления об обрыве. Один на агента: вторая
// пропажа заменяет первую, а не копится строкой рядом.
func agentOffKey(name string) string { return "agent-off:" + name }

// agentGone — связь оборвалась. Молчим про отозванного: человек его сам
// и выключил. Уведомление не сразу, а через `agents.off_notify_s`: за это
// время агент обычно перезванивает, и тогда будить некого.
func (e *Engine) agentGone(name string, held time.Duration) {
	a, ok := e.store.Agent(name)
	if !ok || a.State != store.AgentOK {
		return
	}
	n := agentOffNotice(a, held, time.Now())
	gen := e.agentOffMark(name)
	wait := e.agentOffWait()
	if wait <= 0 {
		e.agentOffDue(name, gen, n)
		return
	}
	time.AfterFunc(wait, func() { e.agentOffDue(name, gen, n) })
}

// agentOffWait — сколько агент может быть без связи до уведомления; 0 — сразу.
func (e *Engine) agentOffWait() time.Duration {
	return time.Duration(e.store.SettingInt(agentOffNotifyDef)) * time.Second
}

// agentOffNotice — само уведомление о пропаже. У шлюза своя беда: память
// и карточки кода тут ни при чём, без него Сёма не открывается снаружи.
func agentOffNotice(a *store.Agent, held time.Duration, at time.Time) *store.Notification {
	title := "Агент «" + a.Name + "» пропал со связи"
	summary := "пока его нет, память и карточки кода не отвечают"
	if AgentRole(a) == RoleGateway {
		title = "Шлюз «" + a.Name + "» без связи"
		summary = "пока его нет, Сёма не открывается снаружи: телефон и браузер вне дома не достучатся"
	}
	return &store.Notification{
		Key: agentOffKey(a.Name), Source: "core", Level: "warn",
		Title: title, Summary: summary,
		Fields: []store.NotifyField{
			{Label: "Последний разговор", Value: at.Format("02.01 15:04")},
			{Label: "Соединение держалось", Value: held.Round(time.Second).String()},
		},
		Actions: []store.NotifyAction{
			{Label: "Открыть агента", Kind: "goto", Screen: AgentScreen(a.Name, "")},
		},
	}
}

// agentOffMark — новая отметка обрыва или возвращения. Отложенное
// уведомление сверяет свою отметку с нынешней: вернулся агент или
// оборвался снова — отметка сменилась, и старый срок молчит.
func (e *Engine) agentOffMark(name string) uint64 {
	e.linkMu.Lock()
	defer e.linkMu.Unlock()
	if e.agentOffGen == nil {
		e.agentOffGen = map[string]uint64{}
	}
	e.agentOffGen[name]++
	return e.agentOffGen[name]
}

// agentOffDue — срок вышел: агент так и не вернулся — говорим человеку.
// Отозвали его за это время — молчим, как и при самом обрыве.
func (e *Engine) agentOffDue(name string, gen uint64, n *store.Notification) {
	e.linkMu.Lock()
	still := e.agentOffGen[name] == gen && e.links[name] == nil
	e.linkMu.Unlock()
	if !still {
		return
	}
	if a, ok := e.store.Agent(name); !ok || a.State != store.AgentOK {
		return
	}
	_, _ = e.Notify(n)
}

// agentBack — агент вернулся. Отложенное уведомление больше не нужно, а
// уже показанное закрывается само и говорит, сколько связи не было:
// человеку важно, проспал мак час или трое суток.
func (e *Engine) agentBack(name, seen string) {
	e.agentOffMark(name)
	away := "неизвестно сколько"
	if t, err := time.Parse(time.RFC3339, seen); err == nil {
		away = time.Since(t).Round(time.Minute).String()
	}
	e.CloseNotification(agentOffKey(name), store.NotifyDone, "связи не было "+away)
}

// AgentMemory — строка про память для экрана «Агенты»: когда спрашивали
// последний раз и сколько раз за сутки. Житель машины становится
// единственной дверью в память, и когда она замолчит, человек пойдёт
// смотреть сюда, а не в журнал памяти на другом экране.
func (e *Engine) AgentMemory(name string) string {
	since := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	n, last := e.store.AgentMemory(name, since)
	if n == 0 {
		return "память: за сутки не спрашивали"
	}
	when := ""
	if t, err := time.Parse(time.RFC3339, last); err == nil {
		when = ", последнее в " + t.Local().Format("15:04")
	}
	return fmt.Sprintf("память: %d за сутки%s", n, when)
}
