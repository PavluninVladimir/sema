package secret

import (
	"strings"
	"testing"
)

func TestPhrase(t *testing.T) {
	p, err := NewPhrase()
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Split(p, "-")) != 7 {
		t.Fatalf("формат: %s", p)
	}
	if back, err := ParsePhrase(strings.ToUpper(strings.ReplaceAll(p, "-", " "))); err != nil || back != p {
		t.Fatalf("фраза не читается без дефисов и в верхнем регистре: %v %q", err, back)
	}
	// опечатка в одном знаке — опечатка, не «неверная фраза»
	bad := []byte(p)
	if bad[0] == 'a' {
		bad[0] = 'b'
	} else {
		bad[0] = 'a'
	}
	if _, err := ParsePhrase(string(bad)); err == nil || !strings.Contains(err.Error(), "опечатка") {
		t.Fatalf("опечатка не поймана: %v", err)
	}
	if LooksLikePhrase("ключ-для-тестов") {
		t.Fatal("произвольный ключ принят за фразу")
	}
}
