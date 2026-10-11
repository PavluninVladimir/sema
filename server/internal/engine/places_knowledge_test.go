package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Приложение собирается одним запросом по всем местам карточки, и
// порядок в нём — точное раньше общего: символ → файл → папка. Папка
// берётся ближайшая заведённая: принцип про `internal/` не едет к каждому
// файлу сервера, если у `internal/api` есть свой.
func TestPlacesKnowledgeOrderAndNearestDir(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	o := Origin{Consumer: e.ChatConsumer()}
	remember := func(text string, pl store.Place) {
		t.Helper()
		if _, err := e.Remember(nil, o, MemoryRecord{Kind: store.MemLesson, Area: "код", Text: text, Place: &pl}); err != nil {
			t.Fatal(err)
		}
	}
	remember("урок про символ buildInbox", store.Place{Repo: "server", File: "internal/api/inbox.go", Symbol: "buildInbox"})
	remember("факт про файл inbox.go", store.Place{Repo: "server", File: "internal/api/inbox.go"})
	remember("принцип пакета экранов: клиент не знает предметной области", store.Place{Repo: "server", Dir: "internal/api", Source: "CLAUDE.md · Server-driven UI"})
	remember("принцип про весь internal", store.Place{Repo: "server", Dir: "internal"})
	remember("чужой урок про другой файл", store.Place{Repo: "server", File: "internal/api/files.go", Symbol: "buildFiles"})

	o2 := e.ExternalOrigin("проба", "/Users/x/repo/sema/server")
	if err := st.SetConsumerAreas(o2.Consumer.ID, []string{"код"}); err != nil {
		t.Fatal(err)
	}
	places := []store.PlaceQuery{{File: "internal/api/inbox.go"}}
	for _, sym := range []string{"buildInbox", "inboxDigest", "inboxRows"} {
		places = append(places, store.PlaceQuery{File: "internal/api/inbox.go", Symbol: sym})
	}
	text := e.PlacesKnowledge(nil, "проба", "/Users/x/repo/sema/server", "server", places)
	if text == "" {
		t.Fatal("приложение пустое")
	}
	iSym := strings.Index(text, "урок про символ")
	iFile := strings.Index(text, "факт про файл")
	iDir := strings.Index(text, "принцип пакета экранов")
	if iSym < 0 || iFile < 0 || iDir < 0 {
		t.Fatalf("не все уровни пришли:\n%s", text)
	}
	if !(iSym < iFile && iFile < iDir) {
		t.Fatalf("порядок не символ → файл → папка:\n%s", text)
	}
	if strings.Contains(text, "про весь internal") {
		t.Fatalf("принцип родительской папки пришёл поверх ближайшей:\n%s", text)
	}
	if strings.Contains(text, "чужой урок") {
		t.Fatalf("урок другого файла попал в карточку:\n%s", text)
	}
	if !strings.Contains(text, "источник: CLAUDE.md · Server-driven UI") {
		t.Fatalf("источник принципа не назван:\n%s", text)
	}
	// файла в internal/engine у папки api нет — его знание приходит по родителю
	other := e.PlacesKnowledge(nil, "проба", "/Users/x/repo/sema/server", "server", []store.PlaceQuery{{File: "internal/engine/queue.go"}})
	if !strings.Contains(other, "про весь internal") || strings.Contains(other, "пакета экранов") {
		t.Fatalf("для internal/engine ближайшая папка — internal:\n%s", other)
	}
	// области не выданы — пусто, как у припоминания
	o3 := e.ExternalOrigin("чужой", "/Users/y/repo/sema/server")
	if err := st.SetConsumerAreas(o3.Consumer.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := e.PlacesKnowledge(nil, "чужой", "/Users/y/repo/sema/server", "server", places); got != "" {
		t.Fatalf("без выданных областей что-то пришло: %q", got)
	}
}

// Предел и строка «ещё N» держатся и при списке мест.
func TestPlacesAttachLimitNamesRest(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	if err := st.SetSetting("code.knowledge_chars", "300"); err != nil {
		t.Fatal(err)
	}
	o := Origin{Consumer: e.ChatConsumer()}
	var places []store.PlaceQuery
	for i := 0; i < 15; i++ {
		sym := "fn" + itoa(i)
		if _, err := e.Remember(nil, o, MemoryRecord{Kind: store.MemLesson, Area: "код",
			Text:  "Урок " + itoa(i) + ": " + strings.Repeat("подробность, ", 5),
			Place: &store.Place{Repo: "server", File: "internal/api/memory.go", Symbol: sym}}); err != nil {
			t.Fatal(err)
		}
		places = append(places, store.PlaceQuery{File: "internal/api/memory.go", Symbol: sym})
	}
	o2 := e.ExternalOrigin("проба", "/Users/x/repo/sema/server")
	_ = st.SetConsumerAreas(o2.Consumer.ID, []string{"код"})
	text := e.AttachToPlaces(nil, "проба", "/Users/x/repo/sema/server", "server", places)
	if n := len([]rune(text)); n > e.CodeKnowledgeLimit() {
		t.Fatalf("приложение %d знаков при пределе %d", n, e.CodeKnowledgeLimit())
	}
	if !strings.Contains(text, "и ещё") {
		t.Fatal("остаток не назван")
	}
}

// Принципы из CLAUDE.md: тот же отпечаток — ничего, новый при старом
// заголовке — новая версия с supersedes, заголовок исчез — «устарело», а
// снятое человеком не заводится снова при том же отпечатке.
func TestAcceptPrinciplesVersionsVanishAndBlock(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	o := e.ExternalOrigin("Claude Code", "/Users/x/repo/sema")
	if err := st.SetConsumerAreas(o.Consumer.ID, []string{"код"}); err != nil {
		t.Fatal(err)
	}
	batch := func(items ...PrincipleItem) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"path": "/Users/x/repo/sema", "repo": "server", "items": items})
		return raw
	}
	queue := PrincipleItem{File: "internal/store/queue.go", Heading: "Очередь фоновой работы", Hash: "h1", Text: "Всё, что делается не сразу, стоит строкой в work_queue."}
	queue2 := queue
	queue2.File = "internal/engine/queue.go"
	ui := PrincipleItem{Dir: "internal/api", Heading: "Server-driven UI", Hash: "u1", Text: "Клиент рендерит компоненты, не зная предметной области."}

	if _, err := e.AcceptPrinciples(nil, "Claude Code", "/Users/x/repo/sema", batch(queue, queue2, ui)); err != nil {
		t.Fatal(err)
	}
	// абзац с двумя путями — одна запись с двумя привязками
	byFile := st.PlacesBySource(PrincipleSource + "Очередь фоновой работы")
	if len(byFile) != 2 || byFile[0].MemoryID != byFile[1].MemoryID {
		t.Fatalf("у абзаца с двумя путями ждали две привязки одной записи: %+v", byFile)
	}
	first := byFile[0].MemoryID
	m, _ := st.MemoryByID(first)
	if !m.Confirmed || m.Area != "код" || PrincipleHash(m) != "h1" {
		t.Fatalf("запись принципа не такая: %+v", m)
	}
	// тот же отпечаток — ничего не меняется
	if _, err := e.AcceptPrinciples(nil, "Claude Code", "/Users/x/repo/sema", batch(queue, queue2, ui)); err != nil {
		t.Fatal(err)
	}
	if again := st.PlacesBySource(PrincipleSource + "Очередь фоновой работы"); len(again) != 2 || again[0].MemoryID != first {
		t.Fatalf("повтор с тем же отпечатком завёл новое: %+v", again)
	}
	// тот же абзац, но путь разрешился иначе — прежняя координата уходит
	moved := queue2
	moved.File, moved.Repo = "internal/engine/queue.go", "server"
	other := queue
	other.File = "server/internal/store/queue.go"
	other.Repo = "sema"
	if _, err := e.AcceptPrinciples(nil, "Claude Code", "/Users/x/repo/sema", batch(other, moved, ui)); err != nil {
		t.Fatal(err)
	}
	if got := st.PlacesBySource(PrincipleSource + "Очередь фоновой работы"); len(got) != 2 || got[0].MemoryID != first {
		t.Fatalf("при том же отпечатке привязки не приведены к присланным: %+v", got)
	}
	if _, err := e.AcceptPrinciples(nil, "Claude Code", "/Users/x/repo/sema", batch(queue, queue2, ui)); err != nil {
		t.Fatal(err)
	}
	if got := st.PlacesBySource(PrincipleSource + "Очередь фоновой работы"); len(got) != 2 || got[0].Repo != "server" || got[0].File != "internal/engine/queue.go" {
		t.Fatalf("возврат прежних путей не восстановил привязки: %+v", got)
	}
	// тот же отпечаток, другая строка итога — правка на месте, без новой версии
	retold := queue
	retold.Text = "Всё, что делается не сразу, стоит строкой в work_queue."
	if _, err := e.AcceptPrinciples(nil, "Claude Code", "/Users/x/repo/sema", batch(retold, queue2, ui)); err != nil {
		t.Fatal(err)
	}
	if m2, _ := st.MemoryByID(first); m2 == nil || m2.Text != retold.Text {
		t.Fatalf("строка итога не обновилась на месте: %+v", m2)
	}
	if got := st.PlacesBySource(PrincipleSource + "Очередь фоновой работы"); got[0].MemoryID != first {
		t.Fatal("правка строки итога завела новую версию")
	}
	// переписали абзац — новая версия, старая в цепочке
	queue.Hash, queue2.Hash = "h2", "h2"
	queue.Text = "Очередь — единственный источник фоновой работы, пара вид+объект уникальна."
	if _, err := e.AcceptPrinciples(nil, "Claude Code", "/Users/x/repo/sema", batch(queue, queue2, ui)); err != nil {
		t.Fatal(err)
	}
	now := st.PlacesBySource(PrincipleSource + "Очередь фоновой работы")
	if len(now) != 2 || now[0].MemoryID == first {
		t.Fatalf("после правки абзаца ждали новую запись: %+v", now)
	}
	nm, _ := st.MemoryByID(now[0].MemoryID)
	if nm.Supersedes != first || PrincipleHash(nm) != "h2" {
		t.Fatalf("новая версия не ссылается на старую: %+v", nm)
	}
	if old, _ := st.MemoryByID(first); old == nil {
		t.Fatal("старая версия пропала из истории")
	}
	// абзац исчез — привязки устарели, запись цела
	if _, err := e.AcceptPrinciples(nil, "Claude Code", "/Users/x/repo/sema", batch(queue, queue2)); err != nil {
		t.Fatal(err)
	}
	for _, pl := range st.MemoryPlaces(func() int64 { p := st.PlacesBySource(PrincipleSource + "Server-driven UI"); return p[0].MemoryID }()) {
		if !pl.Doubtful {
			t.Fatalf("исчезнувший принцип не помечен устаревшим: %+v", pl)
		}
	}
	// вернулся — пометка снимается
	if _, err := e.AcceptPrinciples(nil, "Claude Code", "/Users/x/repo/sema", batch(queue, queue2, ui)); err != nil {
		t.Fatal(err)
	}
	uiPl := st.PlacesBySource(PrincipleSource + "Server-driven UI")
	if len(uiPl) != 1 || uiPl[0].Doubtful {
		t.Fatalf("вернувшийся принцип остался устаревшим: %+v", uiPl)
	}
	// человек снял и просил не заводить: при том же отпечатке не заводится, при новом — заводится
	if err := e.BlockPrinciple("Server-driven UI", "u1", true); err != nil {
		t.Fatal(err)
	}
	_ = st.UnbindPlace(uiPl[0])
	if _, err := e.AcceptPrinciples(nil, "Claude Code", "/Users/x/repo/sema", batch(queue, queue2, ui)); err != nil {
		t.Fatal(err)
	}
	if got := st.PlacesBySource(PrincipleSource + "Server-driven UI"); len(got) != 0 {
		t.Fatalf("снятая привязка завелась снова: %+v", got)
	}
	ui.Hash = "u2"
	if _, err := e.AcceptPrinciples(nil, "Claude Code", "/Users/x/repo/sema", batch(queue, queue2, ui)); err != nil {
		t.Fatal(err)
	}
	if got := st.PlacesBySource(PrincipleSource + "Server-driven UI"); len(got) != 1 {
		t.Fatalf("изменившийся абзац должен завестись заново: %+v", got)
	}
	// без выданной области «код» — отказ
	if _, err := e.AcceptPrinciples(nil, "Codex", "/Users/z/other", batch(ui)); err == nil {
		t.Fatal("принципы приняты без выданной области")
	}
}

// Папку переименовали — привязка к ней помечается «место могло
// переехать», знание остаётся и по-прежнему находится; протухшая по
// сроку привязка снимается, а запись живёт.
func TestRenamedDirMarkedNotLost(t *testing.T) {
	e, st := newTestEngine(t, "тест", nil)
	m, err := e.Remember(nil, Origin{Consumer: e.ChatConsumer()}, MemoryRecord{
		Kind: store.MemFact, Area: "код", Text: "клиент не знает предметной области",
		Place: &store.Place{Repo: "server", Dir: "internal/web", Source: "урок из работы"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDirDoubtful(m.ID, "internal/web", true); err != nil {
		t.Fatal(err)
	}
	place := st.MemoryPlaces(m.ID)
	if len(place) != 1 || !place[0].Doubtful || place[0].Dir != "internal/web" {
		t.Fatalf("привязка к папке не помечена: %+v", place)
	}
	// знание всё ещё приходит, с честной пометкой
	o := e.ExternalOrigin("проба", "/Users/x/repo/sema/server")
	_ = st.SetConsumerAreas(o.Consumer.ID, []string{"код"})
	text := e.PlacesKnowledge(nil, "проба", "/Users/x/repo/sema/server", "server", []store.PlaceQuery{{File: "internal/web/app.go"}})
	if !strings.Contains(text, "предметной области") || !strings.Contains(text, "переехать") {
		t.Fatalf("знание или пометка пропали:\n%s", text)
	}
	// по сроку привязка уходит, запись — нет
	if err := st.SetSetting("code.place_ttl_days", "30"); err != nil {
		t.Fatal(err)
	}
	if err := st.AgePlace(m.ID, time.Now().AddDate(0, 0, -40)); err != nil {
		t.Fatal(err)
	}
	e.parseDoubtfulPlaces()
	if left := st.MemoryPlaces(m.ID); len(left) != 0 {
		t.Fatalf("протухшая привязка к папке не снята: %+v", left)
	}
	if got, err := st.MemoryByID(m.ID); err != nil || got == nil {
		t.Fatal("запись пропала вместе с привязкой")
	}
}
