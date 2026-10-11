package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Отписка показывается только тогда, когда рассылка сама сказала, как
// отписаться. Гадать по ссылкам в теле нельзя: там рядом лежат
// «отписаться» и «удалить аккаунт».
func TestUnsubscribeOnlyByHeader(t *testing.T) {
	случаи := []struct {
		raw, ссылка, почта string
	}{
		{"<https://рассылка.рф/off?id=7>, <mailto:off@рассылка.рф>", "https://рассылка.рф/off?id=7", "off@рассылка.рф"},
		{"<mailto:off@рассылка.рф>, <https://рассылка.рф/off>", "https://рассылка.рф/off", "off@рассылка.рф"},
		{"<https://рассылка.рф/off>", "https://рассылка.рф/off", ""},
		{"", "", ""},
		{"мусор", "", ""},
		// javascript: и прочее к отписке отношения не имеет
		{"<javascript:alert(1)>", "", ""},
	}
	for _, с := range случаи {
		ссылка, почта := parseUnsubscribe(с.raw)
		if ссылка != с.ссылка || почта != с.почта {
			t.Errorf("%q → %q / %q, надо %q / %q", с.raw, ссылка, почта, с.ссылка, с.почта)
		}
	}
}

// Письма без заголовка отписки раздела не получают: пустой раздел
// «Рассылка» на личном письме — это шум.
func TestWithoutTitleNoSection(t *testing.T) {
	if блок := messageUnsubscribe(&store.Message{}); блок != nil {
		t.Error("раздел отписки есть у письма без List-Unsubscribe")
	}
	блок := messageUnsubscribe(&store.Message{ListUnsub: "<https://рассылка.рф/off>"})
	if блок == nil {
		t.Fatal("раздела отписки нет, хотя отправитель сказал, куда идти")
	}
	// Сёма не отправляет письмо за человека: почту она отправлять не умеет
	// вовсе, и делать вид нельзя
	dump := dumpScreen(&Document{Components: []Component{*блок}})
	if strings.Contains(dump, "send") || strings.Contains(dump, "Отправить") {
		t.Errorf("Сёма делает вид, что умеет отправлять письма: %s", dump)
	}
}
