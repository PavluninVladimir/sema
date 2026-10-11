package engine

import (
	"context"
	"testing"
)

// Скрипт из чата видит те же уведомления и напоминания, что модуль:
// подсказка модели обещает sema.remind, значит он должен быть в песочнице.
func TestScriptChatCanRemind(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	res := e.RunScript(context.Background(), "return typeof sema.remind + ':' + typeof sema.attention.remind + ':' + typeof sema.notify;")
	if res.Err != nil || res.Value != "function:function:function" {
		t.Fatalf("напоминания недоступны скрипту чата: %v %v", res.Value, res.Err)
	}
}
