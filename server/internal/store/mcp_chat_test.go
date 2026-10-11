package store

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// Серверы MCP в чате: разовое заполнение для уже заведённых серверов и
// сохранность описаний инструментов.

// HTTP-серверы, заведённые до появления «В чате», открываются чату один раз,
// сервер жителя машины — нет; описаний разовое заполнение не сочиняет.
// Метка не даёт повторить: закрытое человеком потом остаётся закрытым.
func TestSeedMCPChatOnce(t *testing.T) {
	st := testStore(t)
	for _, m := range []*MCPServer{
		{Name: "planner", URL: "https://api.planner.example/mcp", Enabled: true},
		{Name: "wiki", URL: "https://wiki.example.org/mcp", Enabled: true},
		{Name: "агент", Agent: "мак", Can: "код", Enabled: true},
	} {
		if err := st.SaveMCPServer(m); err != nil {
			t.Fatal(err)
		}
	}
	// как на живой базе: колонки уже есть, а метки нет
	if err := st.SetSetting(mcpChatSeedKey, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.seedMCPChat(); err != nil {
		t.Fatal(err)
	}
	sing, _ := st.MCPServer("planner")
	wiki, _ := st.MCPServer("wiki")
	agent, _ := st.MCPServer("агент")
	if !sing.Chat || !wiki.Chat || agent.Chat {
		t.Errorf("В чате: planner %v, wiki %v, агент %v — ждали да, да, нет", sing.Chat, wiki.Chat, agent.Chat)
	}
	if sing.About != "" || sing.Keywords != "" || wiki.About != "" || wiki.Keywords != "" {
		t.Errorf("описание чужого сервера сочинено: %q / %q", wiki.About, wiki.Keywords)
	}

	// человек закрыл сервер от чата — повторное заполнение его не открывает
	wiki.Chat = false
	if err := st.SaveMCPServer(wiki); err != nil {
		t.Fatal(err)
	}
	if err := st.seedMCPChat(); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.MCPServer("wiki"); again.Chat {
		t.Error("метка не удержала повтор: закрытый человеком сервер открылся снова")
	}
}

// Описания и схемы аргументов переживают запись и чтение; неизвестный класс
// данных не записывается как есть, а пустой читается как «обычное».
func TestMCPToolInfoAndClassRoundTrip(t *testing.T) {
	st := testStore(t)
	schema := json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`)
	m := &MCPServer{Name: "s", URL: "http://x/mcp", Enabled: true, Chat: true, Class: "выдуманное",
		Tools:    []string{"task_create"},
		ToolInfo: []MCPTool{{Name: "task_create", Desc: "Создать задачу", Schema: schema}}}
	if err := st.SaveMCPServer(m); err != nil {
		t.Fatal(err)
	}
	got, ok := st.MCPServer("s")
	if !ok || len(got.ToolInfo) != 1 || got.ToolInfo[0].Desc != "Создать задачу" || string(got.ToolInfo[0].Schema) != string(schema) {
		t.Fatalf("описания инструментов: %+v", got)
	}
	if got.Class != ClassPlain || !got.Chat {
		t.Errorf("класс %q, В чате %v", got.Class, got.Chat)
	}
	// перезапись строки без описаний (например, тумблером) не должна стирать
	// то, что прочла предыдущая проверка, если строка прочитана из базы
	got.Enabled = false
	if err := st.SaveMCPServer(got); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.MCPServer("s"); len(again.ToolInfo) != 1 {
		t.Error("тумблер сервера стёр описания инструментов")
	}
	got.Class = ClassSecret
	_ = st.SaveMCPServer(got)
	if again, _ := st.MCPServer("s"); again.Class != ClassSecret {
		t.Errorf("класс данных не сохранился: %q", again.Class)
	}
}

// Подсказки инструментов: запись, замена, стирание одной и всех; SaveMCPServer
// их не пишет, поэтому проверка, читавшая строку раньше отказа, не стирает
// подсказку, появившуюся за время проверки.
func TestMCPToolHintsRoundTripAndSurviveSave(t *testing.T) {
	st := testStore(t)
	if err := st.SaveMCPServer(&MCPServer{Name: "s", URL: "http://x/mcp", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	stale, _ := st.MCPServer("s") // проверка загрузила строку до отказа
	if len(stale.ToolHints) != 0 {
		t.Fatalf("у нового сервера подсказки: %+v", stale.ToolHints)
	}
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	if err := st.SetMCPToolHint("s", "task_update", "  deadline must be a datetime ", at); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMCPToolHint("s", "task_create", "title is required", at); err != nil {
		t.Fatal(err)
	}
	// пустое не записывается
	_ = st.SetMCPToolHint("s", "task_get", "  ", at)
	_ = st.SetMCPToolHint("s", "", "x", at)

	stale.Enabled = false // и сохраняет строку целиком
	if err := st.SaveMCPServer(stale); err != nil {
		t.Fatal(err)
	}
	got, _ := st.MCPServer("s")
	if got.Enabled || len(got.ToolHints) != 2 {
		t.Fatalf("сохранение строки затёрло подсказки или не сохранилось: %+v / %+v", got.Enabled, got.ToolHints)
	}
	if h := got.ToolHints["task_update"]; h.Gist != "deadline must be a datetime" || h.At != at.Format(time.RFC3339) {
		t.Errorf("подсказка: %+v", h)
	}
	// новая суть заменяет прежнюю
	if err := st.SetMCPToolHint("s", "task_update", "deadline needs an offset", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.MCPServer("s"); got.ToolHints["task_update"].Gist != "deadline needs an offset" {
		t.Errorf("суть не заменилась: %+v", got.ToolHints)
	}
	// стереть одну, потом все
	if n, err := st.ForgetMCPToolHints("s", "task_create"); err != nil || n != 1 {
		t.Fatalf("забыть одну: %d %v", n, err)
	}
	if n, _ := st.ForgetMCPToolHints("s", "нет-такого"); n != 0 {
		t.Errorf("забыто несуществующее: %d", n)
	}
	if n, err := st.ForgetMCPToolHints("s", ""); err != nil || n != 1 {
		t.Fatalf("забыть все: %d %v", n, err)
	}
	if got, _ = st.MCPServer("s"); len(got.ToolHints) != 0 {
		t.Errorf("подсказки остались: %+v", got.ToolHints)
	}
	// сервера нет — ошибка, а не молчание
	if err := st.SetMCPToolHint("нет", "t", "x", at); err == nil {
		t.Error("подсказка несуществующему серверу записана")
	}
}

// Отказы от нескольких чатов сразу не затирают друг друга: запись — чтение и
// запись одного JSON под замком.
func TestMCPToolHintsConcurrentWrites(t *testing.T) {
	st := testStore(t)
	if err := st.SaveMCPServer(&MCPServer{Name: "s", URL: "http://x/mcp", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = st.SetMCPToolHint("s", "tool"+string(rune('a'+i)), "суть", time.Now())
		}(i)
	}
	wg.Wait()
	if got, _ := st.MCPServer("s"); len(got.ToolHints) != 12 {
		t.Errorf("записано %d подсказок из 12: %+v", len(got.ToolHints), got.ToolHints)
	}
}
