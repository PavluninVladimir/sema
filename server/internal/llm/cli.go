package llm

// Провайдер типа cli — модель по подписке через официальный клиент вендора
// (Claude Code, Codex). Клиент запускается подпроцессом, промпт уходит ему
// на вход, ответ читается со стандартного вывода. Ключей нет, поэтому и
// шифровать нечего; расплата — лимиты подписки и зависимость от чужой
// программы, установленной на машине.
//
// К веб-сессиям личного кабинета Сёма не обращается: это нарушает условия
// вендоров и ломается при первом их обновлении.

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/config"
)

type cliProvider struct {
	name    string
	command string   // claude | codex | своя программа
	args    []string // аргументы неинтерактивного режима
}

// cliArgs — как позвать известные клиенты без интерактивного режима.
// Это значения по умолчанию: в справочнике провайдеров их можно поменять,
// если вендор изменит вызов.
var cliArgs = map[string][]string{
	"claude": {"-p"},
	"codex":  {"exec"},
}

func newCLI(name string, p config.Provider) *cliProvider {
	cmd := strings.TrimSpace(p.BaseURL)
	if cmd == "" {
		cmd = strings.TrimSpace(p.Model)
	}
	fields := strings.Fields(cmd)
	c := &cliProvider{name: name, command: fields[0]}
	if len(fields) > 1 {
		c.args = fields[1:]
	} else {
		c.args = cliArgs[c.command]
	}
	return c
}

func (c *cliProvider) Name() string { return c.name }

func (c *cliProvider) Complete(ctx context.Context, prompt string) (string, error) {
	path, err := exec.LookPath(c.command)
	if err != nil {
		return "", fmt.Errorf("клиент %q не установлен на этой машине", c.command)
	}
	// клиенту нужно время: он ходит в облако вендора сам
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, append(append([]string{}, c.args...), prompt)...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	started := time.Now()
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		var ferr error
		if strings.Contains(strings.ToLower(msg), "login") || strings.Contains(msg, "auth") {
			ferr = fmt.Errorf("клиент %s требует входа по подписке", c.command)
		} else {
			ferr = fmt.Errorf("клиент %s: %s", c.command, clipLine(msg))
		}
		report(ctx, c.name, "text", prompt, "", Usage{}, started, ferr)
		return "", ferr
	}
	answer := strings.TrimSpace(out.String())
	if answer == "" {
		err := fmt.Errorf("клиент %s ответил пусто за %.0f с", c.command, time.Since(started).Seconds())
		report(ctx, c.name, "text", prompt, "", Usage{}, started, err)
		return "", err
	}
	report(ctx, c.name, "text", prompt, answer, Usage{}, started, nil) // usage клиент не отдаёт — оценка
	return answer, nil
}

// Available — установлен ли клиент; настройки показывают это человеку.
func (c *cliProvider) Available() error {
	_, err := exec.LookPath(c.command)
	if err != nil {
		return fmt.Errorf("не установлен")
	}
	return nil
}

func clipLine(s string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if r := []rune(s); len(r) > 160 {
		return string(r[:160])
	}
	return s
}
