package engine

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

func md5hex(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }

// Вход как в «Сетевом городе»: страница входа ставит cookie, с ним выдаётся
// соль, форма входа принимает md5(соль + md5(пароль в windows-1251)). Модуль
// проходит его сессией: cookie уходит во второй и третий запрос, Set-Cookie
// модулю не виден, ответ 500 — значение, а не исключение, и модуль после
// него не выключен.
func TestModuleHTTPSessionLogin(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	cp, _ := charmap.Windows1251.NewEncoder().String("пароль")
	wantPW2 := md5hex([]byte("соль42" + md5hex([]byte(cp))))
	srv := servePages(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasCookie := func() bool { c, err := r.Cookie("NSSESSIONID"); return err == nil && c.Value == "abc" }
		switch r.URL.Path {
		case "/logindata":
			http.SetCookie(w, &http.Cookie{Name: "NSSESSIONID", Value: "abc", Path: "/"})
			_, _ = w.Write([]byte(`{}`))
		case "/auth/getdata":
			if !hasCookie() {
				http.Error(w, "нет сессии", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"salt":"соль42","lt":"1","ver":"2"}`))
		case "/login":
			_ = r.ParseForm()
			if !hasCookie() || r.PostForm.Get("pw2") != wantPW2 || r.PostForm.Get("un") != "мИванов" {
				http.Error(w, "вход не принят", http.StatusConflict)
				return
			}
			_, _ = w.Write([]byte(`{"at":"токен"}`))
		default:
			http.Error(w, "сломалось", http.StatusInternalServerError)
		}
	}))
	code := `function login() {
  const base = ` + strconv.Quote(srv.URL) + `;
  const s = sema.http.session();
  s.get(base + '/logindata');
  const meta = JSON.parse(s.post(base + '/auth/getdata', '').body);
  const pw2 = sema.crypto.md5(meta.salt + sema.crypto.md5('пароль', {encoding: 'windows-1251'}));
  const r = s.post(base + '/login', {un: 'мИванов', pw2: pw2, pw: pw2.slice(0, 6)}, {contentType: 'application/x-www-form-urlencoded'});
  const boom = s.get(base + '/boom');
  return {status: r.status, ok: r.ok, body: r.body, setCookie: Object.keys(r.headers).indexOf('set-cookie') >= 0,
    boom: boom.status, boomOk: boom.ok, boomErr: boom.error};
}`
	m := &Module{Name: "dnevnik", Needs: []string{"http"}, Code: code}
	if err := e.InstallModule(m); err != nil {
		t.Fatalf("модуль с сессией и хешем не встал: %v", err)
	}
	if err := e.EnableModule(m.Name, true); err != nil {
		t.Fatal(err)
	}
	res, err := e.RunModule(context.Background(), m.Name, "login()")
	if err != nil || res.Err != nil {
		t.Fatalf("вход: %v %v %s", err, res.Err, res.ErrText)
	}
	var got struct {
		Status    int    `json:"status"`
		OK        bool   `json:"ok"`
		Body      string `json:"body"`
		SetCookie bool   `json:"setCookie"`
		Boom      int    `json:"boom"`
		BoomOK    bool   `json:"boomOk"`
		BoomErr   string `json:"boomErr"`
	}
	if err := json.Unmarshal([]byte(res.Value), &got); err != nil {
		t.Fatalf("ответ модуля: %v %s", err, res.Value)
	}
	if got.Status != 200 || !got.OK || got.Body != `{"at":"токен"}` {
		t.Fatalf("вход не прошёл: %+v", got)
	}
	if got.SetCookie {
		t.Error("Set-Cookie виден модулю")
	}
	if got.Boom != 500 || got.BoomOK || got.BoomErr != "HTTP 500" {
		t.Errorf("ответ 500: %+v", got)
	}
	if after, _ := e.Module(m.Name); !after.Enabled || after.Broken != "" {
		t.Errorf("модуль выключен после 500 чужого сервера: %+v", after.Broken)
	}
}

// Без права http сессии нет, а хеши есть всегда.
func TestCryptoNeedsNoRight(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	m := &Module{Name: "hashes", Code: `function h() { return sema.crypto.sha256('abc') + ' ' + sema.crypto.md5('пароль', {encoding: 'windows-1251'}); }`}
	if err := e.InstallModule(m); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableModule(m.Name, true); err != nil {
		t.Fatal(err)
	}
	res, err := e.RunModule(context.Background(), m.Name, "h()")
	if err != nil || res.Err != nil {
		t.Fatalf("хеши: %v %v %s", err, res.Err, res.ErrText)
	}
	cp, _ := charmap.Windows1251.NewEncoder().String("пароль")
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad " + md5hex([]byte(cp))
	if res.Value != strconv.Quote(want) && res.Value != want {
		t.Errorf("хеши: %s, ждали %s", res.Value, want)
	}
	// сессия без права http не ставится вовсе: проверка кода до установки
	noHTTP := &Module{Name: "nohttp", Code: `function s() { return sema.http.session(); }`}
	if err := e.InstallModule(noHTTP); err == nil || !strings.Contains(err.Error(), "http") {
		t.Errorf("сессия без права http: %v", err)
	}
}

// Модель знает, чем входить на сайт с cookie: подсказка о модулях и скилл.
func TestModelKnowsHTTPSession(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	p := e.chatScriptsPrompt()
	for _, want := range []string{"sema.http.session()", "sema.crypto.md5"} {
		if !strings.Contains(p, want) {
			t.Errorf("в подсказке о модулях нет %q", want)
		}
	}
	text, _ := e.skillsPrompt("напиши модуль, который ходит в электронный дневник")
	if !strings.Contains(text, "sema.http.session()") {
		t.Error("скилл «Модуль-интеграция» не говорит про сессию")
	}
}
