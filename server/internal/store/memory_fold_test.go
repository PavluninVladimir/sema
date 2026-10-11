package store

import (
	"strings"
	"testing"
)

// Граф памяти различал регистр, и одна сущность разъезжалась на два узла:
// «pi» и «Pi», «sema» и «Sema» (живая база, 5 октября 2026). Слияние при
// старте оставляет узел с большим числом связей, переносит к нему связи
// второго вместе с записью-источником, убирает петлю между ними и пишет
// строку в журнал памяти; новый узел с другим регистром имени находит
// прежний. Узлы разных областей не сливаются: связи «личного» стали бы
// видны тем, кому выдан только «код».
func TestMemoryNodesMergeCaseDuplicates(t *testing.T) {
	s := testStore(t)
	// узлы как на старой базе: без ключа имени
	add := func(name, area string) int64 {
		t.Helper()
		res, err := s.db.Exec(`INSERT INTO memory_nodes (type, name, area, name_key) VALUES ('сущность', ?, ?, '')`, name, area)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	edge := func(from, to int64, mem int64) {
		t.Helper()
		if err := s.AddMemoryEdge(from, to, "связан", mem); err != nil {
			t.Fatal(err)
		}
	}
	pi, Pi := add("pi", "код"), add("Pi", "код")
	wg, сёма, роутер := add("pi-wg", "код"), add("Сёма", "код"), add("роутер", "код")
	СЁМА := add("СЁМА", "код")
	// одно имя в двух областях — разные узлы, их не сливаем
	домКод, домЛичное := add("дом", "код"), add("Дом", "личное")
	edge(pi, wg, 0)
	edge(pi, сёма, 0)
	edge(Pi, роутер, 0)
	edge(pi, Pi, 0)
	edge(Pi, wg, 7) // та же связь, что у pi, но с записью-источником
	edge(СЁМА, роутер, 0)
	edge(домКод, роутер, 0)
	edge(домЛичное, роутер, 0)

	if err := s.foldMemoryNodes(); err != nil {
		t.Fatal(err)
	}
	nodes, _ := s.MemoryNodes()
	names := map[string]int64{}
	for _, n := range nodes {
		names[n.Name] = n.ID
	}
	// у pi и Pi по три связи — остаётся заведённый раньше
	if _, ok := names["Pi"]; ok || names["pi"] != pi {
		t.Fatalf("остаться должен «pi», узлы: %v", names)
	}
	if _, ok := names["СЁМА"]; ok || names["Сёма"] != сёма {
		t.Fatalf("кириллица: остаться должен «Сёма», узлы: %v", names)
	}
	if names["дом"] != домКод || names["Дом"] != домЛичное {
		t.Fatalf("узлы разных областей слиты: %v", names)
	}
	edges, _ := s.MemoryEdges()
	var toRouter, loops int
	var wgSource int64 = -1
	for _, e := range edges {
		if e.From == e.To {
			loops++
		}
		if (e.From == pi && e.To == роутер) || (e.From == сёма && e.To == роутер) {
			toRouter++
		}
		if e.From == pi && e.To == wg {
			wgSource = e.MemoryID
		}
	}
	if toRouter != 2 || loops != 0 {
		t.Errorf("связи слитых не перенесены или осталась петля: к роутеру %d, петель %d (%+v)", toRouter, loops, edges)
	}
	if wgSource != 7 {
		t.Errorf("у совпавшей связи pi → pi-wg пропала запись-источник: %d", wgSource)
	}
	log, _ := s.MemoryLog(20)
	merges := 0
	for _, e := range log {
		if e.Action == "merge" && strings.Contains(e.Detail, "только регистром") {
			merges++
		}
	}
	if merges != 2 {
		t.Errorf("в журнале памяти %d строк о слиянии, ждали 2", merges)
	}

	// новый «PI» в той же области — тот же узел; в другой области — свой
	if id, err := s.EnsureMemoryNode("сущность", "PI", "код"); err != nil || id != pi {
		t.Errorf("«PI» в «коде» завёлся заново (%d, %v), а должен найтись «pi» (%d)", id, err, pi)
	}
	if id, err := s.EnsureMemoryNode("сущность", "PI", "личное"); err != nil || id == pi {
		t.Errorf("«PI» в «личном» склеился с узлом «кода» (%d, %v)", id, err)
	}
	// «слить с самим собой» удалило бы узел — отказ
	if err := s.MergeMemoryNodes(pi, pi); err == nil {
		t.Error("узел слит сам с собой")
	}
	if _, err := s.MemoryNode(pi); err != nil {
		t.Errorf("после отказа узел пропал: %v", err)
	}
	// повторный запуск ничего не делает
	before, _ := s.MemoryNodes()
	if err := s.foldMemoryNodes(); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.MemoryNodes(); len(after) != len(before) {
		t.Errorf("повторное слияние изменило граф: %d → %d узлов", len(before), len(after))
	}
}
