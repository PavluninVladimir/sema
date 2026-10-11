package api

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

// Заголовок с текущим экраном приходит закодированным — и раскодируется;
// старое незакодированное значение читается как есть.
func TestCurrentScreenIsDecoded(t *testing.T) {
	случаи := map[string]string{
		url.PathEscape("settings:servers~log:хозяин"): "settings:servers~log:хозяин",
		"settings%3Amail%40me%40example.com":          "settings:mail@me@example.com",
		"settings:mail":                               "settings:mail",
		"":                                            "",
	}
	for в, want := range случаи {
		r := httptest.NewRequest("POST", "/do", nil)
		r.Header.Set("HX-Current-Screen", в)
		if has := currentScreen(r); has != want {
			t.Errorf("заголовок %q прочитан как %q, ждали %q", в, has, want)
		}
	}
}
