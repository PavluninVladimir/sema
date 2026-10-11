package engine

// Заголовки в сетевых инструментах и подстановка секретов ядром. Токен не
// попадает в текст модели: в заголовке модуль пишет {{secret:имя}}, а
// значение подставляет Сёма — и только если это имя человек выдал модулю.

import (
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// reSecretRef — {{secret:имя}} в значении заголовка.
var reSecretRef = regexp.MustCompile(`\{\{\s*secret:([^}]+)\}\}`)

// secretFiller — подстановщик секретов для одного запуска: знает, какие
// имена выданы, и пишет каждое обращение в журнал секрета.
type secretFiller func(name string) (string, error)

// moduleSecretFiller — подстановка по правам модуля.
func (e *Engine) moduleSecretFiller(m *Module, who string) secretFiller {
	return func(name string) (string, error) {
		use := store.SecretUse{Name: name, Module: m.Name, Who: who + " · заголовок запроса"}
		if m == nil || !m.HasGrant(name) {
			use.Note = "право не выдано"
			e.store.LogSecret(use)
			return "", fmt.Errorf("секрет %q не выдан — выдайте его на экране «Ключи и секреты»", name)
		}
		sec, ok := e.store.Secret(name)
		if !ok || sec.Kind == store.SecretSSH || sec.Kind == store.SecretOAuth {
			use.Note = "секрета нет, это ключ SSH или токены входа"
			e.store.LogSecret(use)
			return "", fmt.Errorf("секрет %q не выдан — выдайте его на экране «Ключи и секреты»", name)
		}
		v, ok := e.store.SecretValue(name)
		if !ok {
			use.Note = "не расшифровался"
			e.store.LogSecret(use)
			return "", fmt.Errorf("секрет %q не читается — проверьте ключ шифрования сервера", name)
		}
		use.Granted = true
		e.store.LogSecret(use)
		return v, nil
	}
}

// coreSecretFiller — подстановка для самого ядра (справочник MCP):
// прав спрашивать не у кого, но журнал ведётся.
func (e *Engine) coreSecretFiller(who string) secretFiller {
	return func(name string) (string, error) {
		use := store.SecretUse{Name: name, Who: who}
		v, ok := e.store.SecretValue(name)
		if !ok {
			use.Note = "секрета нет или не расшифровался"
			e.store.LogSecret(use)
			return "", fmt.Errorf("секрет %q не читается", name)
		}
		use.Granted = true
		e.store.LogSecret(use)
		return v, nil
	}
}

// fillHeaders — заголовки с подставленными секретами. Ошибка одного имени
// отменяет весь запрос: половина заголовков хуже, чем честный отказ.
func fillHeaders(raw map[string]string, fill secretFiller) (map[string]string, error) {
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		var failed error
		filled := reSecretRef.ReplaceAllStringFunc(v, func(m string) string {
			name := strings.TrimSpace(reSecretRef.FindStringSubmatch(m)[1])
			if fill == nil {
				failed = fmt.Errorf("подстановка секретов здесь недоступна")
				return ""
			}
			val, err := fill(name)
			if err != nil {
				failed = err
				return ""
			}
			return val
		})
		if failed != nil {
			return nil, failed
		}
		out[k] = filled
	}
	return out, nil
}

// maskHeaders — заголовки для журнала: значения с подстановкой не
// показываются, остальные обрезаются.
func maskHeaders(raw map[string]string) string {
	var parts []string
	for k, v := range raw {
		if reSecretRef.MatchString(v) || strings.EqualFold(k, "authorization") {
			parts = append(parts, k+": ●●●")
			continue
		}
		parts = append(parts, k+": "+clip(v, 40))
	}
	return strings.Join(parts, ", ")
}

// headersOpt вынимает {headers: {...}} из аргументов скрипта.
func headersOpt(opts map[string]any) map[string]string {
	out := map[string]string{}
	raw, _ := opts["headers"].(map[string]any)
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// httpRequest — запрос с заголовками. Общая точка для http.get/post/request.
func httpRequest(method, url, body, contentType string, headers map[string]string) (string, error) {
	raw, _, err := httpFetch(method, url, body, contentType, headers)
	return raw, err
}

// httpFetch — тот же запрос, но говорит ещё и адрес, на котором остановились
// после перенаправлений: относительные ссылки страницы считаются от него, а
// не от того, что просили.
func httpFetch(method, url, body, contentType string, headers map[string]string) (string, *neturl.URL, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", nil, fmt.Errorf("нужен адрес вида https://…")
	}
	req, err := http.NewRequest(strings.ToUpper(method), url, strings.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := scriptHTTP.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", nil, err
	}
	if resp.StatusCode >= 400 {
		return "", nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, clip(string(raw), 200))
	}
	return string(raw), resp.Request.URL, nil
}
