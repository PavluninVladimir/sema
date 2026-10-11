package web

import (
	"strings"
	"testing"
)

// Пустоты между тегами ужимаются, а текст внутри <pre> остаётся как был:
// иначе команда на подтверждение потеряла бы отступы.
func TestCollapseGapsDoesNotTouchPre(t *testing.T) {
	input := "<div>\n      \n        <span>а</span>\n   \n<pre class=\"output\">x\n    y\n\n  z</pre>\n\n  </div>"
	got := collapseGaps(input)
	if strings.Contains(got, ">\n      \n") {
		t.Fatalf("пустоты не ужались: %q", got)
	}
	if !strings.Contains(got, "x\n    y\n\n  z") {
		t.Fatalf("текст внутри pre изменился: %q", got)
	}
	if !strings.Contains(got, "<span>а</span>") {
		t.Fatal("теги пострадали")
	}
}
