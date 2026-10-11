package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Правка по функциям: одноимённое заменяется, новое дописывается,
// перечисленное в remove уходит, остальное не трогается ни символом.
func TestPatchModuleByFuncs(t *testing.T) {
	old := "var LIMIT = 3;\n\n// сбор\nfunction collect() { return [1, 2]; }\n\nfunction old() { return 0; }\n\nfunction run() { return collect().length; }\n"
	patch := "var LIMIT = 10;\nfunction collect() {\n  return [1, 2, 3];\n}\nfunction preview() { return collect(); }"
	result, err := ApplyPatch(old, patch, []string{"old"})
	if err != nil {
		t.Fatal(err)
	}
	for _, need := range []string{"var LIMIT = 10;", "return [1, 2, 3];", "function preview()", "function run() { return collect().length; }", "// сбор"} {
		if !strings.Contains(result.Code, need) {
			t.Errorf("в коде нет %q:\n%s", need, result.Code)
		}
	}
	for _, extra := range []string{"var LIMIT = 3", "return [1, 2];", "function old"} {
		if strings.Contains(result.Code, extra) {
			t.Errorf("в коде осталось %q:\n%s", extra, result.Code)
		}
	}
	if strings.Count(result.Code, "function collect") != 1 {
		t.Errorf("collect задвоился:\n%s", result.Code)
	}
	if len(result.Replaced) != 2 || len(result.Added) != 1 || len(result.Removed) != 1 {
		t.Errorf("отчёт правки не тот: %+v", result)
	}
	if _, err := ParseModuleCode(result.Code); err != nil {
		t.Fatalf("код после правки не разбирается: %v", err)
	}
	if _, err := ApplyPatch(old, "collect();", nil); err == nil {
		t.Error("правка без объявлений прошла")
	}
	if _, err := ApplyPatch(old, "function a() {}", []string{"нет_такой"}); err == nil {
		t.Error("убрали несуществующее")
	}
}

// Правка из чата ставит модуль так же, как целиком: манифест меняет только
// названные поля, права и данные остаются, связи проверяются.
func TestPatchModuleFromChat(t *testing.T) {
	model := &nextModel{replies: []string{
		"Поправил сбор.\n\n```sema-module-patch:проба-правки\n{\"tasks\": [{\"title\": \"Подборка\", \"schedule\": \"9:00, 19:00\", \"run\": \"run\"}], \"tools\": [\"preview() — превью\"]}\nfunction collect() { return [1, 2, 3]; }\nfunction preview() { return collect().length; }\n```",
	}}
	e, st := newTestEngine(t, "модель", model)
	m, err := ParseModule(`{"name":"проба-правки","title":"Проба правки","about":"сбор","needs":["store"],
		"tasks":[{"title":"Утро","schedule":"9:00","run":"run"}]}
function collect() { return [1]; }
function run() { sema.store.set("n", collect().length); return 1; }`)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.InstallModule(m); err != nil {
		t.Fatal(err)
	}
	chatID, _ := st.CreateChat("правка")
	if err := e.ChatReply(context.Background(), chatID, "поправь сбор"); err != nil {
		t.Fatal(err)
	}
	fresh, ok := e.Module("проба-правки")
	if !ok {
		t.Fatal("модуль пропал")
	}
	if !strings.Contains(fresh.Code, "return [1, 2, 3]") || !strings.Contains(fresh.Code, "sema.store.set") || !strings.Contains(fresh.Code, "function preview") {
		t.Fatalf("правка не наложилась:\n%s", fresh.Code)
	}
	if len(fresh.Tasks) != 1 || fresh.Tasks[0].Schedule != "9:00, 19:00" || len(fresh.Tools) != 1 {
		t.Fatalf("манифест правки не применился: %+v %v", fresh.Tasks, fresh.Tools)
	}
	if fresh.About != "сбор" || len(fresh.Needs) != 1 {
		t.Fatalf("поля, которых нет в правке, потерялись: about=%q needs=%v", fresh.About, fresh.Needs)
	}
	msgs, _ := st.ChatMsgs(chatID, 20)
	for _, msg := range msgs {
		var card ToolCard
		if msg.Role == "tool" && json.Unmarshal([]byte(msg.Content), &card) == nil && strings.Contains(card.Title, "Проба правки") {
			return
		}
	}
	t.Fatal("карточки модуля в чате нет")
}

// Ответ из одного блока правки говорит правду: включённый модуль
// обновлён и работает, а не «установлен выключенным».
func TestReplyOnPatchSaysTruth(t *testing.T) {
	model := &nextModel{replies: []string{
		"```sema-module-patch:проба-ответа\nfunction run() { return 2; }\n```",
	}}
	e, st := newTestEngine(t, "модель", model)
	m, _ := ParseModule(`{"name":"проба-ответа","title":"Проба ответа","tasks":[{"title":"t","schedule":"9:00","run":"run"}]}
function run() { return 1; }`)
	if err := e.InstallModule(m); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableModule("проба-ответа", true); err != nil {
		t.Fatal(err)
	}
	chatID, _ := st.CreateChat("правка")
	if err := e.ChatReply(context.Background(), chatID, "поправь"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := st.ChatMsgs(chatID, 10)
	for _, msg := range msgs {
		if msg.Role == "assistant" {
			if strings.Contains(msg.Content, "выключенным") || !strings.Contains(msg.Content, "Обновил") {
				t.Fatalf("ответ врёт о состоянии модуля: %q", msg.Content)
			}
			return
		}
	}
	t.Fatal("ответа нет")
}

// Правка модуля, присланная вторым ходом (после команд на сервере),
// применяется, а не остаётся сырым кодом в переписке.
func TestPatchSecondTurnApplied(t *testing.T) {
	model := &nextModel{replies: []string{
		"Посмотрю журнал.\n\n```sema-ssh:проба-сервер\nuptime\n```",
		"Теперь правка.\n\n```sema-module-patch:проба-второй-ход\nfunction run() { return 2; }\n```",
	}}
	e, st := newTestEngine(t, "модель", model)
	m, _ := ParseModule(`{"name":"проба-второй-ход","title":"Проба второго хода","tasks":[{"title":"t","schedule":"9:00","run":"run"}]}
function run() { return 1; }`)
	if err := e.InstallModule(m); err != nil {
		t.Fatal(err)
	}
	chatID, _ := st.CreateChat("правка вторым ходом")
	if err := e.ChatReply(context.Background(), chatID, "посмотри и поправь"); err != nil {
		t.Fatal(err)
	}
	fresh, _ := e.Module("проба-второй-ход")
	if !strings.Contains(fresh.Code, "return 2") {
		t.Fatalf("правка второго хода не наложилась:\n%s", fresh.Code)
	}
	msgs, _ := st.ChatMsgs(chatID, 20)
	for _, msg := range msgs {
		if msg.Role == "assistant" && strings.Contains(msg.Content, "sema-module-patch") {
			t.Fatalf("код правки остался в переписке: %q", msg.Content)
		}
	}
}
