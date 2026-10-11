package store

import (
	"testing"
	"time"
)

// Та же беда приходит снова и заменяет прежнюю запись, а не ложится второй
// строкой: лента одинаковых строк — это и есть способ не заметить беду.
func TestSameFindingNotStoredTwice(t *testing.T) {
	s := testStore(t)
	finding := Finding{Key: "повтор:ocr:file:7", Kind: "повтор без следа", Ref: "ocr:file:7",
		Level: FindingWarn, Summary: "работа повторяется впустую", Numbers: "12 работ",
		Measure: MeasureAlert, Screen: "queue"}

	isNew, err := s.SaveFinding(finding)
	if err != nil || !isNew {
		t.Fatalf("первая запись: новая=%v err=%v", isNew, err)
	}
	finding.Numbers = "48 работ"
	isNew, err = s.SaveFinding(finding)
	if err != nil {
		t.Fatal(err)
	}
	if isNew {
		t.Fatal("повторная находка объявлена новой — человек получит второе уведомление о том же")
	}
	list, err := s.Findings(true, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("открытых находок %d, а должна быть одна", len(list))
	}
	if list[0].Numbers != "48 работ" {
		t.Fatalf("числа не обновились: %q", list[0].Numbers)
	}
	if n, _ := s.OpenFindings(); n != 1 {
		t.Fatalf("счётчик открытых %d", n)
	}
}

// Срок хранения касается только закрытых: беда, которая тянется полгода, тем
// и важна, что тянется полгода.
func TestRetentionKeepsOpenFindings(t *testing.T) {
	s := testStore(t)
	longAgo := time.Now().AddDate(0, 0, -200).Format(time.RFC3339)

	for _, key := range []string{"закрытая", "открытая"} {
		if _, err := s.SaveFinding(Finding{Key: key, Kind: "проба", Ref: key,
			Level: FindingWarn, Summary: key, Measure: MeasureAlert}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CloseFinding("закрытая", "прошло"); err != nil {
		t.Fatal(err)
	}
	// состарим обе записи
	if _, err := s.db.Exec(`UPDATE self_findings SET last_at = ?`, longAgo); err != nil {
		t.Fatal(err)
	}

	if n := s.RemoveOldFindings(0); n != 0 {
		t.Fatalf("нулевой срок означает «не убирать», а убрано %d", n)
	}
	if n := s.RemoveOldFindings(90); n != 1 {
		t.Fatalf("убрано %d записей, а должна быть одна закрытая", n)
	}
	all, err := s.Findings(false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Key != "открытая" {
		t.Fatalf("уборка задела открытую находку: %+v", all)
	}
}
