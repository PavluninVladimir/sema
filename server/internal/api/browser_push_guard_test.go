package api

import (
	"os"
	"strings"
	"testing"
)

// Сторож браузерных тестов пушей. Они гоняют настоящий Chrome, а headless
// Chrome на macOS показывает уведомления по-настоящему — в центр
// уведомлений хозяина машины. 1 октября 2026 первая версия теста засыпала
// его десятками «Диск на srv-bots», и с `requireInteraction` они висели,
// пока их не закрывали руками. Подмена `showNotification` в тесте обязана
// только записывать вызов; стоит ей позвать настоящий метод — и тест снова
// показывает чужие уведомления. Этот тест не требует Chrome и идёт всегда:
// он читает исходник браузерного теста и падает при возврате настоящего показа.
func TestBrowserPushNeverShowsRealNotification(t *testing.T) {
	raw, err := os.ReadFile("browser_push_test.go")
	if err != nil {
		t.Fatalf("исходник браузерного теста пушей не прочитан: %v", err)
	}
	src := string(raw)

	// подмена на месте и она — заглушка, а не обёртка над настоящим методом
	const marker = "self.registration.showNotification = ("
	at := strings.Index(src, marker)
	if at < 0 {
		t.Fatal("в браузерном тесте пушей нет подмены showNotification: без неё работник покажет настоящее уведомление")
	}
	stub := src[at:]
	end := strings.Index(stub, "};")
	if end < 0 {
		t.Fatal("подмена showNotification не заканчивается — сторож не может её разобрать")
	}
	stub = stub[:end]
	for _, banned := range []string{"real(", "real.", ".bind(", "ServiceWorkerRegistration.prototype"} {
		if strings.Contains(stub, banned) {
			t.Errorf("подмена showNotification зовёт настоящий показ (%q): headless Chrome на маке покажет уведомление в центре хозяина", banned)
		}
	}
	if !strings.Contains(stub, "return Promise.resolve()") {
		t.Error("подмена showNotification должна возвращать готовое обещание и больше ничего не делать")
	}

	// и другие дороги к настоящему показу в этом файле закрыты
	for _, banned := range []string{
		"DeliverPushMessage", // пуш по протоколу Chrome: без показа Chrome рисует своё уведомление «по умолчанию»
		"new Notification(",  // страница показывает уведомление сама
		`"push_test"`,        // «Проверить» шлёт пуш в службу
		"TestPushDevice(",    // то же из движка
	} {
		if strings.Contains(src, banned) {
			t.Errorf("в браузерном тесте пушей встретилось %q — дорога к настоящему уведомлению", banned)
		}
	}
}
