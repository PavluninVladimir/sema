package journal

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Журнал человек присылает в переписку, когда что-то сломалось. Значит в
// нём не должно быть ни одного секрета — ни известного, ни такого, о
// котором никто не подумал. Перебираем то, что рядом с логированием живёт
// на самом деле.
func TestSecretsNeverReachLog(t *testing.T) {
	key := "оченьДлинныйКлючШифрования42"
	cases := []struct {
		what   string
		in     string
		banned string
	}{
		{"ключ шифрования", "старт с SEMA_KEY=" + key, key},
		{"заголовок доступа", `запрос: Authorization: Bearer sk-abc123def456ghi789`, "sk-abc123def456ghi789"},
		{"cookie сессии", "Cookie: sema_session=sema_s_ABCDEFGH12345678ijkl", "sema_s_ABCDEFGH12345678ijkl"},
		{"токен Сёмы", "хук пришёл с sema_ABCDEFGH12345678ijklmn", "sema_ABCDEFGH12345678ijklmn"},
		{"закрытый ключ SSH", "ключ: -----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk=\n-----END OPENSSH PRIVATE KEY-----", "b3BlbnNzaC1rZXk="},
		{"пароль в строке подключения", "imap: imaps://vlad:МойПароль123@imap.yandex.ru", "МойПароль123"},
		{"пароль полем", `конфиг: {"password": "секретноеСлово"}`, "секретноеСлово"},
		{"шифрованное значение", "секрет: enc:v1:AAAABBBBCCCCDDDD1234", "enc:v1:AAAABBBBCCCCDDDD1234"},
		// вход OAuth на сервер MCP: токен идёт заголовком, а ответ сервера
		// выдач — JSON с access_token и refresh_token
		{"Bearer-токен входа", "MCP «planner»: HTTP 401, Authorization: Bearer mcp_at_9f8e7d6c5b4a3210zyx", "mcp_at_9f8e7d6c5b4a3210zyx"},
		{"токен в Bearer без заголовка", "ушло: Bearer mcp_at_9f8e7d6c5b4a3210zyx", "mcp_at_9f8e7d6c5b4a3210zyx"},
		{"access_token в JSON", `ответ: {"access_token":"mcp_at_1a2b3c4d5e6f7a8b","token_type":"Bearer"}`, "mcp_at_1a2b3c4d5e6f7a8b"},
		{"refresh_token в JSON", `ответ: {"refresh_token": "mcp_rt_0f1e2d3c4b5a6978"}`, "mcp_rt_0f1e2d3c4b5a6978"},
		{"токены в форме", "POST token: grant_type=refresh_token&refresh_token=mcp_rt_0f1e2d3c4b5a6978&client_id=abc", "mcp_rt_0f1e2d3c4b5a6978"},
		{"проверочный код PKCE", "обмен: code_verifier=dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk", "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"},
		{"запись токенов целиком", `mcp:planner:oauth = {"access_token":"mcp_at_1a2b3c4d5e6f7a8b","refresh_token":"mcp_rt_0f1e2d3c4b5a6978","client_id":"c1"}`, "mcp_rt_0f1e2d3c4b5a6978"},
		{"api key", "провайдер: api_key=pplx-9f8e7d6c5b4a3210", "pplx-9f8e7d6c5b4a3210"},
		// фраза входа и фраза восстановления ключа выглядят одинаково: ни
		// та, ни другая не должна уехать в переписку вместе с журналом
		{"контрольная фраза", "вход: фраза qmxt-4k9r-bfpd-swzh-7n3v-tjc8-gkme не подошла", "qmxt-4k9r-bfpd-swzh-7n3v-tjc8-gkme"},
		// код первой установки проходит только строкой выдачи (Issue); в любой
		// другой строке — запрос, ошибка, дамп тела — его нет
		{"код первой установки", `вход: тело запроса {"code":"setup-qmxt-4k9r-bfpd-swzh"}`, "setup-qmxt-4k9r-bfpd-swzh"},
	}
	for _, c := range cases {
		got := Mask(c.in, []string{key})
		if strings.Contains(got, c.banned) {
			t.Errorf("%s: секрет прошёл в журнал\n  было:  %s\n  стало: %s", c.what, c.in, got)
		}
	}
}

// Код первой установки печатается нарочно — его читают в `docker logs`, — но
// ровно одной строкой выдачи: и в файле, и на экране он там есть, а в любой
// следующей строке спрятан. Остальные секреты в строке выдачи прячутся как
// обычно.
func TestSetupCodeOnlyInIssueLine(t *testing.T) {
	const code = "setup-qmxt-4k9r-bfpd-swzh"
	dir := t.TempDir()
	var stderr, stdout bytes.Buffer
	l, err := Open(dir, 1, 2, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	l.Issue(&stdout, "вход: код первой установки "+code+" · Authorization: Bearer sk-abc123def456ghi789", code)
	for _, line := range []string{"вход: ввели " + code + " с 172.17.0.1\n", "вход: " + strings.ToUpper(code) + "\n"} {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "sema.log"))
	if err != nil {
		t.Fatal(err)
	}
	file := string(raw)
	if n := strings.Count(strings.ToLower(file), code); n != 1 {
		t.Fatalf("код в файле журнала %d раз, ждали ровно один — в строке выдачи:\n%s", n, file)
	}
	if !strings.Contains(stdout.String(), code) {
		t.Fatalf("строка выдачи не дошла до стандартного вывода: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), code) {
		t.Errorf("код ушёл на экран журнала вторым путём: %q", stderr.String())
	}
	if strings.Contains(file, "sk-abc123def456ghi789") || strings.Contains(stdout.String(), "sk-abc123def456ghi789") {
		t.Error("строка выдачи пронесла чужой секрет: маска в ней снята только с кода")
	}
}

// Обычные строки маска не портит: журнал должен оставаться читаемым.
func TestPlainLinesNotSpoiled(t *testing.T) {
	lines := []string{
		"почта: разобрано 12 писем за 3.4 с",
		"агенты: «мак» на связи",
		"очередь: 5 работ, средняя пауза 3 с",
		"telegram: слушаю сообщения от имени +79001234567",
	}
	for _, s := range lines {
		if got := Mask(s, nil); got != s {
			t.Errorf("строку испортило:\n  было:  %s\n  стало: %s", s, got)
		}
	}
}

// Файл поворачивается по размеру, старые хранятся числом, лишние удаляются.
func TestFileRotates(t *testing.T) {
	dir := t.TempDir()
	var screen bytes.Buffer
	l, err := Open(dir, 1, 2, &screen) // предел 1 МБ, держим 2 файла
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	chunk := strings.Repeat("строка журнала ", 5000) + "\n" // ~70 КБ
	for i := 0; i < 40; i++ {                               // ~2.8 МБ
		if _, err := l.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "sema.log.1")); err != nil {
		t.Fatal("файл не повернулся")
	}
	if _, err := os.Stat(filepath.Join(dir, "sema.log.3")); err == nil {
		t.Fatal("лишние файлы не удаляются — журнал съест диск")
	}
	if screen.Len() == 0 {
		t.Fatal("на экран ничего не ушло")
	}
}

// Значение, которое велели прятать, не попадает и в файл, а не только в
// возвращаемую строку.
func TestHideAppliesToFile(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, 1, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Hide("сверхсекретноеЗначение")
	if _, err := l.Write([]byte("проба: сверхсекретноеЗначение внутри\n")); err != nil {
		t.Fatal(err)
	}
	l.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "sema.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "сверхсекретноеЗначение") {
		t.Fatal("секрет оказался в файле журнала")
	}
	if !strings.Contains(string(raw), "скрыто") {
		t.Fatalf("маска не отметилась: %q", string(raw))
	}
}
