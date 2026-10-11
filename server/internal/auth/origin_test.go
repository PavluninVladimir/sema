package auth

import (
	"strings"
	"testing"
)

func TestLoginOrigin(t *testing.T) {
	if DefaultOrigin("0.0.0.0:9001") != "http://localhost:9001" {
		t.Fatal("адрес входа не взял порт прослушивания")
	}
	if w := OriginWarning("", "0.0.0.0:8765", ""); w != "" {
		t.Fatal("незаданный адрес не должен предупреждать")
	}
	if w := OriginWarning("https://sema.local:8443", "0.0.0.0:8765", "0.0.0.0:8443"); w != "" {
		t.Fatalf("адрес защищённого порта сходится, а есть предупреждение: %s", w)
	}
	if w := OriginWarning("https://sema.local:8443", "0.0.0.0:8765", ""); !strings.Contains(w, "https") {
		t.Fatalf("https без защищённого порта не замечен: %q", w)
	}
	if w := OriginWarning("http://localhost:9999", "0.0.0.0:8765", ""); !strings.Contains(w, "9999") || !strings.Contains(w, "прокси") {
		t.Fatalf("чужой порт не замечен: %q", w)
	}
}
