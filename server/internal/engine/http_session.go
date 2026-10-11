package engine

// Сессия HTTP с cookie и хеши в песочнице модуля (изменение
// module-http-session).
//
// 4 октября 2026 поручение «модуль «Дневник»» встало на входе в «Сетевой
// город»: вход держится на cookie (`logindata` ставит NSSESSIONID, с ним
// `auth/getdata` отдаёт соль, с ней `login`), а пароль уходит хешем
// md5(соль + md5(пароль в windows-1251)). `sema.http.*` отдаёт только тело
// ответа, хешей в песочнице не было — модель честно отказалась слать
// запросы наугад. Это граница платформы, а не модуля.

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"strings"

	"golang.org/x/text/encoding/charmap"

	"github.com/PavluninVladimir/sema/server/internal/script"
)

// httpSessionTool — `sema.http.session()`: объект с методами get, post,
// request. Cookie живут в сессии столько, сколько вызов модуля.
func httpSessionTool(fill secretFiller) script.Tool {
	return script.Tool{
		Name: "http.session", Need: "http", Args: "()",
		Desc: "сессия запросов с cookie: объект с методами get(url, {headers}), post(url, body, {headers, contentType}), " +
			"request(method, url, body, {headers, contentType}). Cookie, что поставил сервер, уходят в следующие запросы этой сессии " +
			"(вход на сайт: страница входа → форма → данные). Ответ — {ok, status, headers, body, url, error}: код и ошибка — значения, " +
			"а не исключения; Set-Cookie в headers нет, их держит сессия. body-объект при contentType " +
			"application/x-www-form-urlencoded уходит формой, иначе JSON. В заголовке можно писать {{secret:имя}}",
		Fn: func(a []any) (any, error) {
			jar, err := cookiejar.New(nil)
			if err != nil {
				return nil, err
			}
			cl := &http.Client{Timeout: scriptHTTP.Timeout, Transport: scriptHTTP.Transport,
				CheckRedirect: scriptHTTP.CheckRedirect, Jar: jar}
			do := func(method, url string, body any, opts map[string]any) map[string]any {
				return sessionDo(cl, fill, method, url, body, opts)
			}
			// функции Go goja делает функциями JS; аргументы приходят
			// выгруженными значениями JS (строка, объект, число)
			return map[string]any{
				"get": func(url string, opts map[string]any) map[string]any { return do("GET", url, nil, opts) },
				"post": func(url string, body any, opts any) map[string]any {
					return do("POST", url, body, optsOrType(opts))
				},
				"request": func(method, url string, body any, opts any) map[string]any {
					return do(method, url, body, optsOrType(opts))
				},
			}, nil
		},
	}
}

// optsOrType — третий аргумент post: строка типа содержимого или объект.
func optsOrType(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case string:
		return map[string]any{"contentType": x}
	}
	return map[string]any{}
}

// sessionDo — один запрос сессии. Ошибки — значением ответа: исключение из
// функции Go в песочнице выключило бы модуль как сломанный за чужой сервер.
func sessionDo(cl *http.Client, fill secretFiller, method, url string, body any, opts map[string]any) map[string]any {
	fail := func(status int, err error) map[string]any {
		return map[string]any{"ok": false, "status": status, "headers": map[string]any{}, "body": "", "url": url, "error": err.Error()}
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fail(0, fmt.Errorf("нужен адрес вида https://…"))
	}
	if opts == nil {
		opts = map[string]any{}
	}
	ct, _ := opts["contentType"].(string)
	payload, ct := sessionBody(body, ct)
	headers, err := fillHeaders(headersOpt(opts), fill)
	if err != nil {
		return fail(0, err)
	}
	req, err := http.NewRequest(strings.ToUpper(method), url, strings.NewReader(payload))
	if err != nil {
		return fail(0, err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return fail(0, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fail(resp.StatusCode, err)
	}
	hdr := map[string]any{}
	for k, v := range resp.Header {
		// cookie держит сессия; номер сессии в журнале и у модели не нужен
		if strings.EqualFold(k, "Set-Cookie") {
			continue
		}
		hdr[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	out := map[string]any{"ok": resp.StatusCode < 400, "status": resp.StatusCode, "headers": hdr,
		"body": string(raw), "url": resp.Request.URL.String(), "error": ""}
	if resp.StatusCode >= 400 {
		out["error"] = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return out
}

// sessionBody — тело запроса: строка как есть, объект — формой при типе
// формы, иначе JSON (и тип JSON, если не назван).
func sessionBody(body any, ct string) (string, string) {
	switch b := body.(type) {
	case nil:
		return "", ct
	case string:
		return b, ct
	case map[string]any:
		if strings.HasPrefix(strings.ToLower(ct), "application/x-www-form-urlencoded") {
			form := neturl.Values{}
			for k, v := range b {
				form.Set(k, fmt.Sprint(v))
			}
			return form.Encode(), ct
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Sprint(body), ct
	}
	if ct == "" {
		ct = "application/json"
	}
	return string(raw), ct
}

// cryptoTools — `sema.crypto.md5|sha1|sha256(text, {encoding})`: хеш в
// шестнадцатеричном виде. Права не требует: считает, ничего не трогая.
func cryptoTools() []script.Tool {
	mk := func(name string, sum func([]byte) string) script.Tool {
		return script.Tool{
			Name: "crypto." + name, Args: "(text, {encoding})",
			Desc: "хеш " + strings.ToUpper(name) + " строки в шестнадцатеричном виде; encoding — кодировка байтов строки: " +
				"«utf-8» (по умолчанию) или «windows-1251» (как у старых порталов с паролем в cp1251)",
			Fn: func(a []any) (any, error) {
				enc, _ := script.Opts(a, 1)["encoding"].(string)
				raw, err := encodeText(script.Str(a, 0), enc)
				if err != nil {
					return nil, err
				}
				return sum(raw), nil
			},
		}
	}
	return []script.Tool{
		mk("md5", func(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }),
		mk("sha1", func(b []byte) string { s := sha1.Sum(b); return hex.EncodeToString(s[:]) }),
		mk("sha256", func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }),
	}
}

func encodeText(text, enc string) ([]byte, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(enc), "_", "-")) {
	case "", "utf-8", "utf8":
		return []byte(text), nil
	case "windows-1251", "cp1251", "cp-1251":
		raw, err := charmap.Windows1251.NewEncoder().String(text)
		if err != nil {
			return nil, fmt.Errorf("строку не записать в windows-1251: %w", err)
		}
		return []byte(raw), nil
	}
	return nil, fmt.Errorf("кодировку %q не знаю: «utf-8» или «windows-1251»", enc)
}
