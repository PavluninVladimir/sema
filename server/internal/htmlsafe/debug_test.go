package htmlsafe

import "testing"

func TestDebug(t *testing.T) {
	r := Clean(`<p>Счёт</p><img src="https://track/a.gif"><img src="http://b/c.png">`, false)
	t.Logf("картинок=%d html=%q", r.Images, r.HTML)
}
