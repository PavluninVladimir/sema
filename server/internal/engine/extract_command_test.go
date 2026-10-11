package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Дочерний процесс изображает настоящий распознаватель: проверяется
// ограничение именно на пути os/exec, а не только отдельный writer.
func TestExtractCommandChild(t *testing.T) {
	if len(os.Args) < 2 {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "audit-output":
		fmt.Print(strings.Repeat("x", 2*extractOutputLimit))
	case "audit-sleep":
		time.Sleep(5 * time.Second)
	case "audit-text":
		fmt.Print("прочитанный текст")
	default:
		return
	}
	os.Exit(0)
}

func TestExtractCommandBoundsOutputAndTime(t *testing.T) {
	args := []string{"-test.run=^TestExtractCommandChild$", "--"}
	// вывод больше предела — берём начало и останавливаем процесс, а не
	// теряем текст целиком: у длинного договора текста больше мегабайта
	out, err := extractCommand(context.Background(), os.Args[0], append(args, "audit-output")...)
	if err != nil || len(out) != extractOutputLimit {
		t.Fatalf("вывод должен обрезаться до предела: %d байт, %v", len(out), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = extractCommand(ctx, os.Args[0], append(args, "audit-sleep")...)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatalf("распознаватель не остановлен по сроку: %v (%v)", err, time.Since(started))
	}
	out, err = extractCommand(context.Background(), os.Args[0], append(args, "audit-text")...)
	if err != nil || string(out) != "прочитанный текст" {
		t.Fatalf("обычный вывод потерян: %q, %v", out, err)
	}
}
