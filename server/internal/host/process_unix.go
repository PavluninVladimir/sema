//go:build !windows

package host

import (
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

func cpuTimeOS() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(int64(ru.Utime.Sec)+int64(ru.Stime.Sec))*time.Second +
		time.Duration(int64(ru.Utime.Usec)+int64(ru.Stime.Usec))*time.Microsecond
}

func diskFreeOS(path string) int64 {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0
	}
	return int64(fs.Bavail) * int64(fs.Bsize)
}

func openInBrowserOS(addr string) {
	if runtime.GOOS == "darwin" {
		_ = exec.Command("open", addr).Start()
		return
	}
	_ = exec.Command("xdg-open", addr).Start() // Raspberry Pi и прочий Linux
}
