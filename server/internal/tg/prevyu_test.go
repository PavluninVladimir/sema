package tg

import (
	"testing"

	"github.com/gotd/td/tg"
)

// Отправку поста с картинкой-превью ждём, пока Telegram соберёт страницу:
// «в работе» и пустота готовым превью не считаются.
func TestPreviewReady(t *testing.T) {
	cases := []struct {
		name  string
		media tg.MessageMediaClass
		want  bool
	}{
		{"собрано", &tg.MessageMediaWebPage{Webpage: &tg.WebPage{URL: "https://x"}}, true},
		{"в работе", &tg.MessageMediaWebPage{Webpage: &tg.WebPagePending{}}, false},
		{"пусто", &tg.MessageMediaWebPage{Webpage: &tg.WebPageEmpty{}}, false},
		{"нет превью", &tg.MessageMediaEmpty{}, false},
		{"ничего", nil, false},
	}
	for _, c := range cases {
		if got := previewReady(c.media); got != c.want {
			t.Errorf("%s: %v, а ждали %v", c.name, got, c.want)
		}
	}
}
