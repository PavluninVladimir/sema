// Пакет version — версия сборки Сёмы. Зашивается при сборке
// (`-ldflags -X …/internal/version.Version=v1.2.3`); без неё берётся то,
// что знает сам Go о репозитории: короткий отпечаток коммита и время.
// Версия видна на «Здоровье», в `-doctor` и в ответе /api/health — по ней
// видно, что за сборка стоит у человека, когда он присылает журнал.
package version

import (
	"runtime/debug"
	"strings"
)

// Version — задаётся сборкой; пусто — сборка без метки (go run, тесты).
var Version string

// String — версия для показа: метка сборки, иначе коммит из сведений Go,
// иначе «без версии».
func String() string {
	if v := strings.TrimSpace(Version); v != "" {
		return v
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		rev, dirty, when := "", "", ""
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "+"
				}
			case "vcs.time":
				when = s.Value
			}
		}
		if rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			if when != "" {
				return rev + dirty + " · " + when[:10]
			}
			return rev + dirty
		}
	}
	return "без версии"
}
