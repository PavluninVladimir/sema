package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Длинная схема по-русски обрывалась на середине: предел стоял в байтах,
// а кириллица занимает вдвое больше. Последний вопрос не режется вовсе.
func TestHistoryChatCutByRunes(t *testing.T) {
	long := strings.Repeat("схема выключения VPN на Микротике ", 120) // ~4000 знаков
	if len([]rune(long)) < 3000 || len(long) < 6000 {
		t.Fatalf("проба не годится: %d рун, %d байт", len([]rune(long)), len(long))
	}
	// старая реплика: режется по рунам и говорит об этом
	cut := clipHistory(long, historyLimit)
	r := []rune(cut)
	if len(r) < historyLimit || !strings.Contains(cut, "обрезано") {
		t.Fatalf("старая реплика обрезана неверно: %d рун", len(r))
	}
	if !strings.HasPrefix(cut, "схема выключения") {
		t.Fatal("начало реплики потеряно")
	}
	// последний вопрос той же длины остаётся целым
	if got := clipHistory(long, lastMsgLimit); got != long {
		t.Fatalf("последний вопрос обрезан: %d из %d рун", len([]rune(got)), len([]rune(long)))
	}
}

// Щедрый предел — последней реплике человека: заметка «страницу прочитать
// не удалось» после вопроса не должна отнимать у него предел.
func TestLastUserMessageKeepsLimitBehindNote(t *testing.T) {
	history := []*store.ChatMsg{
		{Role: "user", Content: "старый вопрос"},
		{Role: "assistant", Content: "старый ответ"},
		{Role: "user", Content: "длинное задание"},
		{Role: "note", Content: "Страницу https://x.example прочитать не удалось: HTTP 401"},
	}
	if got := lastUserIndex(history); got != 2 {
		t.Fatalf("последняя реплика человека: %d, ждали 2", got)
	}
	if got := lastUserIndex([]*store.ChatMsg{{Role: "note", Content: "одна заметка"}}); got != 0 {
		t.Fatalf("без реплик человека — последняя строка: %d", got)
	}
}
