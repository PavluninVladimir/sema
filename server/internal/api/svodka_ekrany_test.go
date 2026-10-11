package api

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Проверки языка «Сводка» на трёх экранах: «Что ушло», первый запуск и граф
// памяти. Смотрим состав документа — ровно то, что увидит клиент. Разметку
// глазами тут не проверить: правки доезжают до браузера только выкатом.

// pills — тексты всех капсул компонента.
func pills(c Component) []string {
	var out []string
	for _, b := range c.Badges {
		out = append(out, b.Text)
	}
	return out
}

// firstKind — первый компонент такого вида, включая вложенные (у соседей
// это `найти`, но тесты должны работать и по отдельности).
func firstKind(cs []Component, kind string) *Component { return findIn(cs, kind) }

func allKinds(cs []Component, kind string) []Component {
	var out []Component
	for i := range cs {
		if cs[i].Type == kind {
			out = append(out, cs[i])
		}
		out = append(out, allKinds(cs[i].Children, kind)...)
	}
	return out
}

// ——— «Что ушло» ———

// Счётчики за сутки стояли строкой в заголовке раздела: «За сутки · наружу
// 92 из 198 · чувствительных 30 · …». Читать такое человек не станет —
// числа переехали в шапку, каждое со своей подписью.
func TestSentNumbersInHeader(t *testing.T) {
	s, st := testServer(t)
	if err := st.AddSent(&store.SentRow{Consumer: "core:queue", Model: "qwen3", OK: true,
		Class: store.ClassPlain, TokensIn: 262, TokensOut: 418, Seconds: 57.8}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen(sentScreen+"~"+sentTabLog, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	числа := firstKind(doc.Components, "figs")
	if числа == nil {
		t.Fatal("на «Что ушло» нет строки чисел")
	}
	if len(числа.Children) < 4 {
		t.Errorf("в шапке %d чисел, ждали хотя бы четыре", len(числа.Children))
	}
	for _, ч := range числа.Children {
		if ч.Title == "" || ч.Text == "" {
			t.Errorf("число без подписи или без значения: %+v", ч)
		}
	}
	for _, c := range doc.Components {
		if c.Type == "section" && strings.Contains(c.Title, "наружу") && strings.Contains(c.Title, "из") {
			t.Errorf("счётчики так и остались в заголовке раздела: %q", c.Title)
		}
	}
}

// Строка отправки: то, что Сёма померил, — одной строкой меты (клиент
// рисует её моноширинным), а капсула достаётся только исключению. «Своя
// сеть» у каждой из двухсот строк не отличала ничего.
func TestSentNormIsSilent(t *testing.T) {
	s, st := testServer(t)
	обычная := &store.SentRow{Consumer: "core:poll", Model: "qwen3", OK: true,
		Class: store.ClassPlain, TokensIn: 262, TokensOut: 418, Seconds: 57.8, Sources: "письма"}
	секретная := &store.SentRow{Consumer: "chat:7", Model: "gpt", OK: true, Outside: true,
		Class: store.ClassSecret, TokensIn: 10, TokensOut: 20, Seconds: 1.2}
	if err := st.AddSent(обычная); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSent(секретная); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen(sentScreen+"~"+sentTabLog, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var норма, исключение *Component
	for _, r := range rowsOf(doc.Components) {
		switch r.Subtitle {
		case "Ядро · poll":
			c := r
			норма = &c
		case "Чат 7":
			c := r
			исключение = &c
		}
	}
	if норма == nil || исключение == nil {
		t.Fatalf("не нашлись строки отправок: %v", rowsOf(doc.Components))
	}
	if len(норма.Badges) != 0 {
		t.Errorf("у обычной отправки капсулы %v — норма должна молчать", pills(*норма))
	}
	if норма.Level != "" || норма.Dot != "" {
		t.Errorf("у обычной отправки горит точка %q/%q", норма.Level, норма.Dot)
	}
	if норма.Meta == "" {
		t.Error("нет строки меты: время, модель, токены и секунды показывать негде")
	}
	for _, что := range []string{"qwen3", "262", "418", "57,8", "контекст: письма"} {
		if !strings.Contains(норма.Meta, что) {
			t.Errorf("в мете %q нет %q", норма.Meta, что)
		}
	}
	if len(норма.Facts) != 0 {
		t.Error("факты парами вернулись в строку списка: они и раздували её втрое")
	}
	if got := pills(*исключение); len(got) != 2 || got[0] != "наружу" || got[1] != "секретное" {
		t.Errorf("у исключения капсулы %v, ждали «наружу» и «секретное»", got)
	}
	if исключение.Level != LevelAlert {
		t.Errorf("секретное наружу без тревоги: уровень %q", исключение.Level)
	}
}

// Очередь памяти пишет отправки десятками подряд, и за ними не видно
// единственной строки с чужим классом. Одинаковые подряд сворачиваются в
// группу, исключение в неё не попадает никогда.
func TestSentIdenticalCollapse(t *testing.T) {
	s, st := testServer(t)
	for i := 0; i < 5; i++ {
		if err := st.AddSent(&store.SentRow{Consumer: "core:queue", Model: "qwen3", OK: true,
			Class: store.ClassPlain, TokensIn: 100, TokensOut: 50, Seconds: 2}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddSent(&store.SentRow{Consumer: "core:queue", Model: "qwen3", OK: true, Outside: true,
		Class: store.ClassSensitive, TokensIn: 100, TokensOut: 50, Seconds: 2}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen(sentScreen+"~"+sentTabLog, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	группы := allKinds(doc.Components, "rowgroup")
	if len(группы) != 1 {
		t.Fatalf("групп %d, ждали одну", len(группы))
	}
	if len(группы[0].Children) != 5 {
		t.Errorf("в группе %d строк, ждали пять", len(группы[0].Children))
	}
	// свёрнутая группа живёт только внутри раздела: полка рисует детей
	// плоско и группу не развернёт
	вРазделе := false
	for _, c := range doc.Components {
		if c.Type == "section" {
			for _, ч := range c.Children {
				if ч.Type == "rowgroup" {
					вРазделе = true
				}
			}
		}
	}
	if !вРазделе {
		t.Error("группа лежит мимо раздела")
	}
	for _, r := range rowsOf(группы[0].Children) {
		if len(r.Badges) != 0 {
			t.Errorf("в кучу попало исключение: %v", pills(r))
		}
	}
}

// Отдельная отправка открывается со своим обратным путём: заголовок у всех
// разный, и без пути непонятно, откуда сюда пришли.
func TestSentInFullWithPath(t *testing.T) {
	s, st := testServer(t)
	if err := st.AddSent(&store.SentRow{Consumer: "core:memory", Model: "qwen3", OK: true,
		Class: store.ClassPlain, TokensIn: 10, TokensOut: 5, Seconds: 1, Prompt: "текст запроса"}); err != nil {
		t.Fatal(err)
	}
	list, err := st.SentList(false, "", "", 1)
	if err != nil || len(list) == 0 {
		t.Fatalf("отправка не записалась: %v", err)
	}
	doc, err := s.buildScreen(sentScreen+"~"+strconv.FormatInt(list[0].ID, 10), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	путь := firstKind(doc.Components, "crumb")
	if путь == nil {
		t.Fatal("нет обратного пути")
	}
	if len(путь.Actions) == 0 || путь.Actions[0].Target != sentScreen+"~"+sentTabLog {
		t.Errorf("путь ведёт не на «Что ушло»: %+v", путь.Actions)
	}
	if firstKind(doc.Components, "figs") == nil {
		t.Error("токены и время не вынесены в шапку")
	}
}

// ——— первый запуск ———

// emptyServer — Сёма, которого ещё не настраивали: ни ящика, ни модели.
func emptyServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	old, had := os.LookupEnv("SEMA_KEY")
	_ = os.Setenv("SEMA_KEY", "ключ-для-тестов")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SEMA_KEY", old)
			return
		}
		_ = os.Unsetenv("SEMA_KEY")
	})
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("база: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng, err := engine.New(&config.Config{Data: dir, KeyEnv: "SEMA_KEY"}, st)
	if err != nil {
		t.Fatalf("движок: %v", err)
	}
	return New(eng), st
}

// Мастер первого запуска: шаги вместо одинаковых строк с капсулами. Видно
// разом, что сделано, что делается сейчас и что впереди, а поля подключения
// лежат внутри своего шага, а не общим куском ниже.
func TestFirstRunBySteps(t *testing.T) {
	s, _ := emptyServer(t)
	doc, err := s.buildScreen("inbox", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	шаги := allKinds(doc.Components, "step")
	if len(шаги) != 2 {
		t.Fatalf("шагов %d, ждали два", len(шаги))
	}
	if шаги[0].Style != "open" || шаги[1].Style != "next" {
		t.Errorf("состояния шагов: %q и %q, ждали open и next", шаги[0].Style, шаги[1].Style)
	}
	if шаги[0].Text != "1" || шаги[1].Text != "2" {
		t.Errorf("номера шагов: %q и %q", шаги[0].Text, шаги[1].Text)
	}
	if findIn(шаги[0].Children, "form") == nil {
		t.Error("форма ящика лежит не внутри своего шага")
	}
	for _, ш := range шаги {
		if ш.Subtitle == "" || ш.Caption == "" {
			t.Errorf("шаг без названия или без итога: %+v", ш)
		}
	}
}

// Сделанный шаг говорит, что получилось на самом деле («owner@…»), а не
// «готово»: галочку рисует клиент, а человеку нужен результат.
func TestFirstRunCompletedStep(t *testing.T) {
	s, st := emptyServer(t)
	if err := st.SaveAccounts([]*store.Account{{Name: "проба", Email: "проба@пример.рф",
		Host: "imap.пример.рф", Port: 993}}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("inbox", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	шаги := allKinds(doc.Components, "step")
	if len(шаги) != 2 {
		t.Fatalf("шагов %d, ждали два", len(шаги))
	}
	if шаги[0].Style != "done" {
		t.Errorf("шаг с подключённым ящиком не сделан: %q", шаги[0].Style)
	}
	if !strings.Contains(шаги[0].Caption, "проба@пример.рф") {
		t.Errorf("сделанный шаг не назвал ящик: %q", шаги[0].Caption)
	}
	if шаги[1].Style != "open" {
		t.Errorf("шаг модели не раскрылся: %q", шаги[1].Style)
	}
	if len(шаги[0].Badges) != 0 {
		t.Errorf("на сделанном шаге капсула %v — галочки достаточно", pills(шаги[0]))
	}
}

// ——— граф памяти ———

// Узлы без единой связи занимали половину картинки и ничего не значили:
// рисовать связи там, где их нет, нечем. Они уходят строкой капсул под
// графом, а в графе остаются только связанные.
func TestGraphLonersNotInPicture(t *testing.T) {
	s, st := testServer(t)
	a, err := st.EnsureMemoryNode("сущность", "Сёма", "код")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.EnsureMemoryNode("сущность", "Raspberry Pi", "код")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureMemoryNode("сущность", "htmx 4", "код"); err != nil {
		t.Fatal(err)
	}
	mid, err := st.SaveMemory(&store.Memory{Kind: store.MemLesson, Area: "код",
		Text: "Сёма живёт на Raspberry Pi", Agent: "Claude Code · ~/repo/sema"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddMemoryEdge(a, b, "живёт на", mid); err != nil {
		t.Fatal(err)
	}
	doc, err := s.buildScreen("memory:graph", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	граф := firstKind(doc.Components, "graph")
	if граф == nil || граф.Graph == nil {
		t.Fatal("графа на экране нет")
	}
	for _, n := range граф.Graph.Nodes {
		if n.Label == "htmx 4" {
			t.Error("одиночка осталась в картинке")
		}
	}
	ряд := firstKind(doc.Components, "chipset")
	if ряд == nil {
		t.Fatal("одиночек некуда положить: ряда капсул нет")
	}
	if len(ряд.Badges) != 1 || ряд.Badges[0].Text != "htmx 4" {
		t.Errorf("в ряду капсул %v", pills(*ряд))
	}
	if ряд.Caption == "" {
		t.Error("ряд капсул не объясняет, почему узел стоит отдельно")
	}
	if !strings.Contains(doc.Subheading, "без единой связи") {
		t.Errorf("подпись экрана молчит про одиночек: %q", doc.Subheading)
	}
}

// Карточка узла: связи сгруппированы по записям, из которых выросли, — у
// каждой видно, кто и когда это утверждал. Раньше запись приписывалась
// хвостом к каждой строке связи и повторялась в ней по десять раз.
func TestNodeCardGroupsByRecords(t *testing.T) {
	s, st := testServer(t)
	центр, err := st.EnsureMemoryNode("сущность", "Сёма", "код")
	if err != nil {
		t.Fatal(err)
	}
	mid, err := st.SaveMemory(&store.Memory{Kind: store.MemLesson, Area: "код",
		Text: "Сёма живёт на Raspberry Pi и говорит с жителем машины", Agent: "Claude Code · ~/repo/sema"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Raspberry Pi", "житель машины"} {
		id, err := st.EnsureMemoryNode("сущность", name, "код")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AddMemoryEdge(центр, id, "связан с", mid); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := s.buildScreen("memory:node:"+strconv.FormatInt(центр, 10), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var откуда *Component
	for i := range doc.Components {
		if doc.Components[i].Type == "section" && doc.Components[i].Title == "Откуда эти связи" {
			откуда = &doc.Components[i]
		}
	}
	if откуда == nil {
		t.Fatal("нет раздела «Откуда эти связи»")
	}
	группы := allKinds(откуда.Children, "rowgroup")
	if len(группы) != 1 {
		t.Fatalf("групп %d, ждали одну — обе связи выросли из одной записи", len(группы))
	}
	if !strings.Contains(группы[0].Subtitle, "Raspberry Pi") {
		t.Errorf("группа не названа текстом записи: %q", группы[0].Subtitle)
	}
	if !strings.Contains(группы[0].Caption, "Claude Code") {
		t.Errorf("не видно, кто это утверждает: %q", группы[0].Caption)
	}
	if len(группы[0].Children) != 2 {
		t.Errorf("в группе %d связей, ждали две", len(группы[0].Children))
	}
	for _, r := range группы[0].Children {
		if len(r.Actions) == 0 || r.Actions[0].Action != "$goto" {
			t.Errorf("из связи некуда перейти: %+v", r.Actions)
		}
	}
	var соседи *Component
	for _, c := range allKinds(doc.Components, "chipset") {
		if c.Title == "С кем связан" {
			к := c
			соседи = &к
		}
	}
	if соседи == nil || len(соседи.Badges) != 2 {
		t.Error("нет ряда капсул «С кем связан» с обоими соседями")
	}
}
