package engine

// Инструменты, которые дают модули: манифест перечисляет их в `tools`
// («attention.remind(text, when, source) — напомнить»), а функция с именем
// после точки становится sema.<модуль>.<функция> в скриптах чата и в других
// модулях (по праву «modules»). Вызов идёт через песочницу модуля с его
// правами — чужих прав инструмент не получает.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/script"
)

// parseToolSpec разбирает строку манифеста: имя, аргументы, описание.
func parseToolSpec(spec string) (name, args, desc string) {
	spec = strings.TrimSpace(spec)
	if i := strings.Index(spec, "—"); i >= 0 {
		desc = strings.TrimSpace(spec[i+len("—"):])
		spec = strings.TrimSpace(spec[:i])
	} else if i := strings.Index(spec, " - "); i >= 0 {
		desc = strings.TrimSpace(spec[i+3:])
		spec = strings.TrimSpace(spec[:i])
	}
	if i := strings.Index(spec, "("); i >= 0 {
		args = spec[i:]
		spec = strings.TrimSpace(spec[:i])
	}
	return spec, args, desc
}

// moduleTools — инструменты всех включённых модулей, кроме самого
// вызывающего: модуль не зовёт себя через песочницу.
func (e *Engine) moduleTools(except string) []script.Tool {
	var out []script.Tool
	for _, m := range e.Modules() {
		if !m.Enabled || m.Name == except {
			continue
		}
		for _, spec := range m.Tools {
			name, args, desc := parseToolSpec(spec)
			if name == "" {
				continue
			}
			fn := name
			if i := strings.LastIndex(name, "."); i >= 0 {
				fn = name[i+1:]
			}
			if !validIdent(fn) {
				continue
			}
			if !strings.Contains(name, ".") {
				name = m.Name + "." + name
			}
			if desc == "" {
				desc = "инструмент модуля «" + m.Title + "»"
			}
			mod, call := m.Name, fn
			out = append(out, script.Tool{
				Name: name, Need: "modules", Args: args, Desc: desc,
				Fn: func(a []any) (any, error) { return e.callModuleTool(mod, call, a) },
			})
		}
	}
	return out
}

func (e *Engine) callModuleTool(module, fn string, args []any) (any, error) {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		raw, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		parts = append(parts, string(raw))
	}
	// предел — тот же, что у скрипта, который инструмент зовёт
	// (scripts.timeout_s): было 30 секунд в коде, и превью поста канала —
	// сбор, поиск, текст моделью — останавливалось всегда
	ctx, cancel := context.WithTimeout(context.Background(), e.ScriptTimeout())
	defer cancel()
	res, err := e.RunModuleFor(ctx, module, fn+"("+strings.Join(parts, ", ")+")", "инструмент «"+module+"."+fn+"»")
	if err != nil {
		return nil, err
	}
	if res.Err != nil {
		return nil, fmt.Errorf("%s", res.ErrText)
	}
	var v any
	if json.Unmarshal([]byte(res.Value), &v) == nil {
		return v, nil
	}
	return res.Value, nil
}

// CallModuleTool — вызов функции модуля из кода ядра: так ядро спрашивает у
// модулей то, что понимают только они (суммы чеков, сроки). Нет модуля,
// выключен или не ответил за отведённое время — пустой ответ без ошибки:
// экран собирается без его данных.
// RequireModuleTool — вызов, который обязан случиться. Мягкий
// CallModuleTool глотает ошибку и отдаёт nil: для украшения экрана это
// правильная деградация, а для работы — тихая потеря. Там, где от вызова
// зависит судьба задачи, нужен этот.
func (e *Engine) RequireModuleTool(ctx context.Context, module, fn string, args ...any) (any, error) {
	m, ok := e.Module(module)
	if !ok {
		return nil, fmt.Errorf("модуль %q не установлен", module)
	}
	if !m.Enabled {
		return nil, fmt.Errorf("модуль %q выключен", module)
	}
	if !validIdent(fn) {
		return nil, fmt.Errorf("недопустимое имя функции %q", fn)
	}
	parts := make([]string, 0, len(args))
	for _, a := range args {
		raw, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		parts = append(parts, string(raw))
	}
	cctx, cancel := context.WithTimeout(ctx, e.ModuleTaskTimeout())
	defer cancel()
	res, err := e.RunModuleFor(cctx, module, fn+"("+strings.Join(parts, ", ")+")", "ядро · "+fn)
	if err != nil {
		return nil, err
	}
	if res.Err != nil {
		return nil, res.Err
	}
	var v any
	if json.Unmarshal([]byte(res.Value), &v) != nil {
		return res.Value, nil
	}
	return v, nil
}

func (e *Engine) CallModuleTool(ctx context.Context, module, fn string, args ...any) any {
	m, ok := e.Module(module)
	if !ok || !m.Enabled || !validIdent(fn) {
		return nil
	}
	parts := make([]string, 0, len(args))
	for _, a := range args {
		raw, err := json.Marshal(a)
		if err != nil {
			return nil
		}
		parts = append(parts, string(raw))
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := e.RunModuleFor(cctx, module, fn+"("+strings.Join(parts, ", ")+")", "ядро · "+fn)
	if err != nil || res.Err != nil || strings.TrimSpace(res.Value) == "" {
		return nil
	}
	var v any
	if json.Unmarshal([]byte(res.Value), &v) != nil {
		return res.Value
	}
	return v
}
