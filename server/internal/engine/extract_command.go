package engine

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

const extractOutputLimit = 1 << 20

var errExtractOutputLimit = errors.New("слишком много текста от программы разбора")

// extractCommand ограничивает срок и вывод внешнего распознавателя.
// Обрезать строку после Output поздно: весь вывод уже занял память Go.
//
// Предел вывода — не отказ, а обрезка: у трёхсотстраничного договора
// текста больше мегабайта, и терять его целиком из-за длины хуже, чем
// взять начало — дальше по цепочке текст всё равно режется под модель.
// Процесс при этом останавливается: остаток никому не нужен.
func extractCommand(parent context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	out := &extractOutput{cancel: cancel}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = out
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if out.exceeded {
		return out.data.Bytes(), nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return out.data.Bytes(), nil
}

type extractOutput struct {
	data     bytes.Buffer
	cancel   context.CancelFunc
	exceeded bool
}

func (w *extractOutput) Write(p []byte) (int, error) {
	if room := extractOutputLimit - w.data.Len(); len(p) > room {
		w.data.Write(p[:room]) // начало остаётся, остальное не читаем
		w.exceeded = true
		w.cancel()
		return 0, errExtractOutputLimit
	}
	return w.data.Write(p)
}
