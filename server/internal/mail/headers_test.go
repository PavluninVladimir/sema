package mail

import (
	"strings"
	"testing"
)

// Заголовки конверта разбираем сами: их всего два, а приезжают они куском
// текста, который может быть оборван на середине.
func TestHeaderParsing(t *testing.T) {
	header := "References: <a@почта.рф> <b@почта.рф>\r\n" +
		" <c@почта.рф>\r\n" +
		"List-Unsubscribe: <https://рассылка.рф/off?id=7>,\r\n" +
		" <mailto:off@рассылка.рф>\r\n"
	refs, unsub := parseHeader(header)
	if len(refs) != 3 {
		t.Errorf("ссылок цепочки %d, а их 3: %v", len(refs), refs)
	}
	// скобки сняты: Яндекс отдаёт без них, Gmail со скобками, а сравнивать
	// приходится одинаково
	for _, r := range refs {
		if strings.ContainsAny(r, "<>") {
			t.Errorf("скобки не сняты: %q", r)
		}
	}
	// продолжение длинной строки приклеено, а не потеряно
	if !strings.Contains(unsub, "mailto:off@рассылка.рф") {
		t.Errorf("хвост List-Unsubscribe потерян: %q", unsub)
	}
	if !strings.Contains(unsub, "https://рассылка.рф/off?id=7") {
		t.Errorf("адрес отписки потерян: %q", unsub)
	}
}

// Оборванная шапка не должна ронять разбор и не должна выдумывать данные.
func TestHeaderWithoutValues(t *testing.T) {
	for _, raw := range []string{"", "References:", "Мусор без двоеточия\r\n", "References: <a@б.рф"} {
		refs, unsub := parseHeader(raw)
		if unsub != "" {
			t.Errorf("на %q придумался адрес отписки %q", raw, unsub)
		}
		if raw == "References: <a@б.рф" && len(refs) != 1 {
			t.Errorf("оборванная ссылка потеряна: %v", refs)
		}
	}
}

// Message-ID сравниваются без скобок и без пустых значений.
func TestCleanIDs(t *testing.T) {
	got := cleanIDs([]string{"<a@б.рф>", "  ", "c@д.рф <e@ж.рф>"})
	if len(got) != 3 || got[0] != "a@б.рф" || got[2] != "e@ж.рф" {
		t.Errorf("чистыеИД дал %v", got)
	}
}
