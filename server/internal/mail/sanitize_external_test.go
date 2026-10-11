package mail

import (
	"strings"
	"testing"
)

// Аудит 7 сентября 2026: чистка блокировала src у картинок, но пропускала
// <link>, @import, <meta refresh> и вложенные документы — отправитель
// узнавал о прочтении и мог увести со страницы.
func TestSanitizeRemovesExternalResources(t *testing.T) {
	for _, payload := range []string{
		`<link rel="stylesheet" href="https://attacker.invalid/style.css">`,
		`<style>@import "https://attacker.invalid/style.css";</style>`,
		`<meta http-equiv="refresh" content="0;url=https://attacker.invalid/">`,
		`<iframe src="https://attacker.invalid/"></iframe>`,
		`<base href="https://attacker.invalid/">`,
	} {
		if out := SanitizeHTML(payload); strings.Contains(out, "attacker.invalid") {
			t.Errorf("осталось: %q -> %q", payload, out)
		}
	}
	// тело письма между внешними тегами остаётся целым
	for _, c := range []struct{ in, keep string }{
		{`<link rel="stylesheet"><p>Здравствуйте, вот важный текст письма.</p><object></object><p>после</p>`, "важный текст письма"},
		{`<base href="https://x"><p>текст</p><iframe src="https://x"></iframe><p>хвост</p>`, "текст"},
	} {
		if out := SanitizeHTML(c.in); !strings.Contains(out, c.keep) || strings.Contains(out, "<object") || strings.Contains(out, "<iframe") {
			t.Errorf("чистка съела текст или оставила тег: %q -> %q", c.in, out)
		}
	}
	// картинка остаётся, но не грузится, пока человек не попросит
	if out := SanitizeHTML(`<img src="https://attacker.invalid/pixel">`); strings.Contains(out, `<img src=`) {
		t.Errorf("картинка грузится сама: %q", out)
	}
}
