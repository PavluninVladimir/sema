package engine

// Имена устройств с пушами. Браузер присылает строку вроде «Mozilla/5.0
// (iPhone; CPU iPhone OS 17_5 like Mac OS X) …», а человеку в списке нужно
// «iPhone · Сёма с экрана «Домой»»: по имени он узнаёт, какой из трёх
// пробных пушей пришёл на какой аппарат. Имя выводится при подписке и
// хранится готовым; своё имя человек задаёт отдельно, и оно главнее.

import (
	"net/url"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// PushDeviceName — имя устройства по строке браузера. standalone — окно
// открыто как установленное приложение («Домой» на iPhone, «Установить» в
// Chrome); touch — сколько касаний понимает экран: у iPad Safari по
// умолчанию представляется маком, и отличает его только касание.
// endpoint нужен запасным путём: у подписок, заведённых до появления имён,
// в строке браузера нет самого браузера, но служба пушей его выдаёт.
func PushDeviceName(ua string, standalone bool, touch int, endpoint string) string {
	low := strings.ToLower(ua)
	var ios string // iPhone | iPad, если это устройство Apple с iOS
	var platform string
	switch {
	case strings.Contains(low, "iphone") || strings.Contains(low, "ipod"):
		ios = "iPhone"
	case strings.Contains(low, "ipad"):
		ios = "iPad"
	case strings.Contains(low, "android"):
		platform = "Android"
	case strings.Contains(low, "macintosh") || strings.Contains(low, "mac os x"):
		if touch > 1 {
			ios = "iPad"
		} else {
			platform = "маке"
		}
	case strings.Contains(low, "windows"):
		platform = "Windows"
	case strings.Contains(low, "cros"):
		platform = "ChromeOS"
	case strings.Contains(low, "linux") || strings.Contains(low, "x11"):
		platform = "Linux"
	}

	browser := pushBrowser(low)
	if browser == "" {
		browser = pushBrowserByService(endpoint)
	}
	if ios != "" {
		if standalone {
			return ios + " · Сёма с экрана «Домой»"
		}
		if browser == "" {
			browser = "Safari"
		}
		return ios + " · " + browser
	}
	if browser == "" {
		browser = "Браузер"
	}
	name := browser
	if platform != "" {
		name += " на " + platform
	}
	if standalone {
		name += " · приложение"
	}
	return name
}

// pushBrowser — браузер по строке (уже в нижнем регистре). Порядок важен:
// Chrome в строке называют все браузеры на его движке, Safari — все
// браузеры на WebKit.
func pushBrowser(low string) string {
	switch {
	case strings.Contains(low, "edg/") || strings.Contains(low, "edga/") || strings.Contains(low, "edgios/"):
		return "Edge"
	case strings.Contains(low, "opr/") || strings.Contains(low, "opera"):
		return "Opera"
	case strings.Contains(low, "samsungbrowser"):
		return "Samsung Internet"
	case strings.Contains(low, "yabrowser"):
		return "Яндекс Браузер"
	case strings.Contains(low, "firefox/") || strings.Contains(low, "fxios/"):
		return "Firefox"
	case strings.Contains(low, "chrome/") || strings.Contains(low, "crios/"):
		return "Chrome"
	case strings.Contains(low, "version/") && strings.Contains(low, "safari/"):
		return "Safari"
	}
	return ""
}

// pushBrowserByService — какой браузер стоит за службой пушей. Только
// запасной путь: Chrome и Edge приходят через одну и ту же Google, и «Chrome»
// здесь — лучшая догадка, а не знание.
func pushBrowserByService(endpoint string) string {
	switch pushServiceKind(endpoint) {
	case "apple":
		return "Safari"
	case "google":
		return "Chrome"
	case "mozilla":
		return "Firefox"
	case "microsoft":
		return "Edge"
	}
	return ""
}

// pushServiceKind — чья служба пушей отвечает за адрес.
func pushServiceKind(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.HasSuffix(host, "push.apple.com"):
		return "apple"
	case strings.HasSuffix(host, "googleapis.com"):
		return "google"
	case strings.HasSuffix(host, "push.services.mozilla.com") || strings.HasSuffix(host, "mozaws.net"):
		return "mozilla"
	case strings.HasSuffix(host, "notify.windows.com"):
		return "microsoft"
	}
	return ""
}

// PushServiceName — как называть службу пушей человеку: «Apple», «Google».
// Её ответ человек видит в строке устройства, и «ответила Apple» говорит
// больше, чем адрес.
func PushServiceName(endpoint string) string {
	switch pushServiceKind(endpoint) {
	case "apple":
		return "Apple"
	case "google":
		return "Google"
	case "mozilla":
		return "Mozilla"
	case "microsoft":
		return "Microsoft"
	}
	if u, err := url.Parse(endpoint); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "служба пушей"
}

// PushDeviceLabel — как устройство называется в списке: имя от человека,
// иначе выведенное при подписке, иначе выводимое сейчас из того, что есть.
// Последнее нужно подпискам, заведённым до появления имён: в их `Agent`
// лежит кусок строки браузера («Macintosh; Intel Mac OS X 10_15_7»).
func PushDeviceLabel(p *store.PushSubscription) string {
	if t := strings.TrimSpace(p.Title); t != "" {
		return clip(t, 80)
	}
	agent := strings.TrimSpace(p.Agent)
	// готовые имена не содержат «;» и «/», а сырая строка браузера содержит
	if agent != "" && !strings.ContainsAny(agent, ";/") {
		return agent
	}
	return PushDeviceName(agent, false, 0, p.Endpoint)
}
