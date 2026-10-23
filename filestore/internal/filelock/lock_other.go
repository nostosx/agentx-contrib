//go:build !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !windows

package filelock

import (
	"errors"
	"os"
)

func tryLock(_ *os.File) (bool, error) {
	return false, errors.New("file locking is unsupported on this platform")
}
