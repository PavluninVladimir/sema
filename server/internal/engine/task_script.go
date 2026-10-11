package engine

import (
	"fmt"
	"strings"
)

// Задача и её скрипт живут вместе. Скрипт «из чата» заводится под одну
// задачу, и когда задачу удаляют, он остаётся сиротой: в «Скриптах»
// копятся «Письма за сутки · из чата» в четырёх экземплярах, и человек не
// знает, какой из них живой. Поэтому связь двусторонняя.

// DeleteTask — удалить задачу вместе с её скриптом, если тот больше никому
// не нужен. Возвращает название удалённого скрипта или пусто.
func (e *Engine) DeleteTask(id int64) (string, error) {
	t, err := e.store.Task(id)
	if err != nil || t == nil {
		return "", fmt.Errorf("задачи %d нет", id)
	}
	scriptID := t.ScriptID
	if err := e.store.DeleteTask(id); err != nil {
		return "", err
	}
	if scriptID == 0 {
		return "", nil
	}
	// скрипт остаётся, если его запускает ещё какая-то задача
	if others, err := e.store.TasksByScript(scriptID); err != nil || len(others) > 0 {
		return "", err
	}
	sc, err := e.store.Script(scriptID)
	if err != nil || sc == nil {
		return "", nil
	}
	if err := e.store.DeleteScript(scriptID); err != nil {
		return "", err
	}
	return sc.Title, nil
}

// ScriptUsedBy — какие задачи запускают скрипт, названиями.
// ScriptUsers — какие задачи запускают какие скрипты, одним проходом: панель
// «Скриптов» спрашивала об этом запросом на каждый из 88 скриптов (perf-pass 5).
func (e *Engine) ScriptUsers() map[int64][]string {
	out := map[int64][]string{}
	tasks, err := e.store.Tasks()
	if err != nil {
		return out
	}
	for _, t := range tasks {
		if t.ScriptID > 0 {
			out[t.ScriptID] = append(out[t.ScriptID], t.Title)
		}
	}
	return out
}

func (e *Engine) ScriptUsedBy(scriptID int64) []string {
	list, err := e.store.TasksByScript(scriptID)
	if err != nil {
		return nil
	}
	var out []string
	for _, t := range list {
		out = append(out, t.Title)
	}
	return out
}

// DeleteScript — скрипт, который запускает задача, сам по себе не
// удаляется: иначе задача останется без кода и будет падать каждый раз по
// расписанию. Удалять надо задачу — она заберёт скрипт с собой.
func (e *Engine) DeleteScript(id int64) error {
	if used := e.ScriptUsedBy(id); len(used) > 0 {
		return fmt.Errorf("скрипт запускает задача «%s» — удалите задачу, она заберёт скрипт с собой", strings.Join(used, "», «"))
	}
	return e.store.DeleteScript(id)
}
