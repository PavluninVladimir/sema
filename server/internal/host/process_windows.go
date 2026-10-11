//go:build windows

package host

import (
	"os/exec"
	"time"
)

// Под Windows эти сведения берутся другими вызовами. Установщик отложен,
// поэтому здесь честные нули, а не выдуманные числа: пустая графа на
// «Здоровье» понятнее, чем правдоподобная ложь.
func cpuTimeOS() time.Duration { return 0 }

func diskFreeOS(path string) int64 { return 0 }

func openInBrowserOS(addr string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", addr).Start()
}
