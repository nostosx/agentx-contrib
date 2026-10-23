package filelock

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func tryLock(f *os.File) (bool, error) {
	const (
		lockExclusiveAndFailImmediately = 0x3
		errorLockViolation              = syscall.Errno(33)
	)
	var overlapped syscall.Overlapped
	result, _, err := lockFileEx.Call(f.Fd(), lockExclusiveAndFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result != 0 {
		return true, nil
	}
	if errors.Is(err, errorLockViolation) {
		return false, nil
	}
	return false, err
}
