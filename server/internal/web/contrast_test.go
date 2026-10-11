package web

// Контраст тихих подписей держится тестом, а не глазом: подписи групп
// меню («Сёма сам», «Своё») набраны цветом --text-faint одиннадцатым кеглем, и
// 4,35:1 приёмка 12 сентября 2026 назвала первой из бед доступности.
// Требование AA для мелкого текста — 4,5:1, и на обоих фонах: экрана и меню.

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"testing"
)

func luminance(hex string) float64 {
	v, _ := strconv.ParseUint(hex[1:], 16, 32)
	channel := func(c float64) float64 {
		c /= 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(float64(v>>16&255)) + 0.7152*channel(float64(v>>8&255)) + 0.0722*channel(float64(v&255))
}

func contrastRatio(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// themeTokens — значения токенов из блока: первый блок :root — светлая
// тема, блок под prefers-color-scheme: dark — тёмная.
func themeTokens(css, start string) map[string]string {
	i := regexp.MustCompile(quoteMeta(start)).FindStringIndex(css)
	if i == nil {
		return nil
	}
	chunk := css[i[1]:]
	if j := regexp.MustCompile(`\n\s*\}`).FindStringIndex(chunk); j != nil {
		chunk = chunk[:j[0]]
	}
	out := map[string]string{}
	for _, m := range regexp.MustCompile(`(--[^:\s]+):\s*(#[0-9A-Fa-f]{6})`).FindAllStringSubmatch(chunk, -1) {
		out[m[1]] = m[2]
	}
	return out
}

func quoteMeta(s string) string { return regexp.QuoteMeta(s) }

func TestQuietLabelContrast(t *testing.T) {
	b, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	for theme, start := range map[string]string{"светлая": ":root {", "тёмная": "prefers-color-scheme: dark"} {
		tokens := themeTokens(css, start)
		if tokens == nil || tokens["--text-faint"] == "" || tokens["--bg"] == "" || tokens["--menu-bg"] == "" {
			t.Fatalf("%s тема: не нашёл --text-faint, --bg или --menu-bg: %v", theme, tokens)
		}
		for _, fg := range []string{"--text-faint", "--text-muted", "--text-secondary", "--text"} {
			for _, bg := range []string{"--bg", "--menu-bg", "--card"} {
				if tokens[fg] == "" || tokens[bg] == "" {
					continue
				}
				if ratio := contrastRatio(tokens[fg], tokens[bg]); ratio < 4.5 {
					t.Errorf("%s тема: %s %s на %s %s — %.2f:1, а нужно 4,5", theme, fg, tokens[fg], bg, tokens[bg], ratio)
				}
			}
		}
	}
}
