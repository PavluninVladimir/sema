package engine

// Обрыв связи с жителем машины — событие, а не тишина. Соединение
// постоянное, поэтому обрыв известен точно: не надо гадать по молчанию и
// заводить порог. Уведомление появляется по обрыву и закрывается само,
// когда агент вернулся.
//
// Уровень «внимание», а не тревога: мак спит по ночам, и ночной обрыв —
// норма жизни, а не поломка. Кому агент сейчас не нужен, тот выключает
// его на экране «Агенты» — у выключенного обрыв молчит.

import (
	"fmt"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// agentOffKey — ключ уведомления об обрыве. Один на агента: вторая
// пропажа заменяет первую, а не копится строкой рядом.
func agentOffKey(name string) string { return "agent-off:" + name }

// agentGone — связь оборвалась. Молчим про отозванного: человек его сам
// и выключил.
func (e *Engine) agentGone(name string, held time.Duration) {
	a, ok := e.store.Agent(name)
	if !ok || a.State != store.AgentOK {
		return
	}
	_, _ = e.Notify(&store.Notification{
		Key: agentOffKey(name), Source: "core", Level: "warn",
		Title:   "Агент «" + name + "» пропал со связи",
		Summary: "пока его нет, память и карточки кода не отвечают",
		Fields: []store.NotifyField{
			{Label: "Последний разговор", Value: time.Now().Format("02.01 15:04")},
			{Label: "Соединение держалось", Value: held.Round(time.Second).String()},
		},
		Actions: []store.NotifyAction{
			{Label: "Открыть агента", Kind: "goto", Screen: AgentScreen(name, "")},
		},
	})
}

// agentBack — агент вернулся. Уведомление закрывается само и говорит,
// сколько связи не было: человеку важно, проспал мак час или трое суток.
func (e *Engine) agentBack(name, seen string) {
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
