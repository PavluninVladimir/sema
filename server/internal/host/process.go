package host

// Снимок процесса: сколько занято памяти, сколько съедено процессора,
// сколько свободно на диске. Всё это спрашивают у операционной системы
// по-разному, и раньше оно лежало посреди `engine/usage.go` — из-за чего
// пакет `engine` не собирался под Windows и ломал `go vet` даже без
// сборки. Здесь этому место: `host` — про машину, а не про предметную
// область.

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ProcessRSS — сколько байт занято. На Linux честно из `/proc`, иначе
// приблизительно по данным среды выполнения: соврать здесь не страшно,
// а не показать ничего — плохо.
func ProcessRSS() int64 {
	if data, err := os.ReadFile("/proc/self/statm"); err == nil {
		f := strings.Fields(string(data))
		if len(f) > 1 {
			if pages, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				return pages * int64(os.Getpagesize())
			}
		}
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int64(ms.Sys)
}

// CPUTime — сколько процессорного времени съел процесс.
func CPUTime() time.Duration { return cpuTimeOS() }

// DiskFree — сколько байт свободно там, где лежат данные.
func DiskFree(path string) int64 {
	return diskFreeOS(path)
}

// FileSize — ноль, если файла нет: у вызывающих это не ошибка.
func FileSize(path string) int64 {
	if st, err := os.Stat(path); err == nil {
		return st.Size()
	}
	return 0
}

// OpenInBrowser — показать страницу человеку. Способ зависит от
// системы, поэтому место ему здесь, а не в коде входа в Gmail.
func OpenInBrowser(addr string) {
	openInBrowserOS(addr)
}
