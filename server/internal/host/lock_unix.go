//go:build !windows

package host

import (
	"os"
	"syscall"
)

// acquireOS — блокировка средствами ОС. Не ждём: занято — значит занято.
func acquireOS(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func releaseOS(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
