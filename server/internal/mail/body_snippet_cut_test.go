package mail

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Письмо robot@delivery.example 7 сентября 2026: тело целиком base64, сниппет
// взят из обрезанного тела, и обрез пришёлся в середину буквы. Раньше
// utf8.Valid на всём результате отвергал расшифровку, и в списке писем
// стояло «0KHQv9Cw0YHQuNCx0L4s…».
func TestBase64SnippetCutMidLetter(t *testing.T) {
	text := "Спасибо, что выбираете Деловые Линии. Заявка № 5520471 создана. Ждём вас в терминале завтра в 10:00."
	full := base64.StdEncoding.EncodeToString([]byte(text))
	// строки по 60 знаков, как в письме, и обрез посреди — как у сниппета
	var b strings.Builder
	for i := 0; i < len(full); i += 60 {
		j := i + 60
		if j > len(full) {
			j = len(full)
		}
		b.WriteString(full[i:j])
		b.WriteString("\r\n")
	}
	cut := b.String()[:len(full)-len(full)%4-9] + "…"
	got := maybeDecodeBase64(cut)
	if strings.HasPrefix(got, "0K") || !strings.HasPrefix(got, "Спасибо, что выбираете Деловые Линии") {
		t.Fatalf("сниппет не расшифрован: %q", got)
	}
}
