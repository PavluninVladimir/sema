//go:build windows

package host

import (
	"fmt"
	"os"
)

// На Windows замок берётся через LockFileEx. Установщик под Windows отложен
// (проверять негде), поэтому здесь честный отказ, а не молчаливое «занял»:
// притворяться, что каталог заперт, опаснее, чем сказать правду.
func acquireOS(f *os.File) error {
	return fmt.Errorf("замок на Windows пока не поддержан")
}

func releaseOS(f *os.File) error { return nil }
