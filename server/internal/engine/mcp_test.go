package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Правила вызовов MCP: у каждого инструмента свой ответ, незнакомый —
// «Запретить» (ответ для нового), и это верно при любом написании имени.
func TestClassifyMCP(t *testing.T) {
	m := &store.MCPServer{
		Name: "трекер",
		Rules: map[string]string{
			"task_list": PolicyAllow, "project_get": PolicyAllow, "get_my_context": PolicyAllow,
			"task_create": PolicyAsk, "task_archive": PolicyDeny, "project_delete_all": PolicyDeny,
		},
	}
	cases := map[string]string{
		"task_list":          PolicyAllow,
		"project_get":        PolicyAllow,
		"get_my_context":     PolicyAllow,
		"task_create":        PolicyAsk,
		"task_archive":       PolicyDeny,
		"project_delete_all": PolicyDeny,
		// имя у сервера может быть записано иначе — правило находится
		"taskList":    PolicyAllow,
		"task-create": PolicyAsk,
		// нового инструмента в правилах нет: ответ по умолчанию
		"task_update": PolicyDeny,
		"":            PolicyDeny,
	}
	for tool, want := range cases {
		if got, why := ClassifyMCP(m, tool); got != want {
			t.Errorf("ClassifyMCP(%q) = %q (%s), хотели %q", tool, got, why, want)
		}
	}
	// ответ для нового инструмента — настройка сервера
	m.RuleDefault = store.RuleAsk
	if got, _ := ClassifyMCP(m, "task_update"); got != PolicyAsk {
		t.Errorf("ответ по умолчанию «Спросить» не действует: %q", got)
	}
}

// Инструмент с «Запретить» модель не видит: его нет ни в описи, ни в полном
// списке сигнатур, а «Спросить» и «Разрешить» остаются. Новый инструмент,
// пока про него не решили, скрыт так же.
func TestVisibleMCPHidesDenied(t *testing.T) {
	m := &store.MCPServer{Name: "трекер",
		Tools:    []string{"task_list", "task_create", "task_delete", "task_brand_new"},
		ToolInfo: []store.MCPTool{{Name: "task_list"}, {Name: "task_create"}, {Name: "task_delete"}, {Name: "task_brand_new"}},
		Rules:    map[string]string{"task_list": PolicyAllow, "task_create": PolicyAsk, "task_delete": PolicyDeny}}
	got := visibleMCP(m)
	if strings.Join(got.Tools, ",") != "task_list,task_create" {
		t.Errorf("модель видит: %v", got.Tools)
	}
	if len(got.ToolInfo) != 2 {
		t.Errorf("в сигнатурах: %v", got.ToolInfo)
	}
	if len(m.Tools) != 4 {
		t.Error("запись сервера изменена: видимость должна быть копией")
	}
}

// Заголовки: {{secret:имя}} заменяется значением, а невыданное имя отменяет
// весь запрос — половина заголовков хуже честного отказа.
func TestFillHeaders(t *testing.T) {
	fill := func(name string) (string, error) {
		if name == "token:трекер" {
			return "секретное-значение", nil
		}
		return "", errors.New("не выдан")
	}
	out, err := fillHeaders(map[string]string{"Authorization": "Bearer {{secret:token:трекер}}", "X-Plain": "да"}, fill)
	if err != nil {
		t.Fatalf("подстановка: %v", err)
	}
	if out["Authorization"] != "Bearer секретное-значение" || out["X-Plain"] != "да" {
		t.Fatalf("заголовки: %+v", out)
	}
	if _, err := fillHeaders(map[string]string{"Authorization": "Bearer {{secret:чужой}}"}, fill); err == nil {
		t.Fatal("невыданный секрет подставился")
	}
	if _, err := fillHeaders(map[string]string{"A": "{{secret:любой}}"}, nil); err == nil {
		t.Fatal("подстановка без права сработала")
	}
}

// В журнал и на экран значение с секретом не показывается.
func TestMaskHeaders(t *testing.T) {
	got := maskHeaders(map[string]string{"Authorization": "Bearer abc123"})
	if got != "Authorization: ●●●" {
		t.Errorf("маска заголовка: %q", got)
	}
	if maskHeaders(map[string]string{"X-Plain": "да"}) != "X-Plain: да" {
		t.Error("обычный заголовок скрыт зря")
	}
}
