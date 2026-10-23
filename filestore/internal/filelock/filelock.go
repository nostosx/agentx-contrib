// Package filelock coordinates processes through persistent sidecar files.
package filelock

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// Acquire exclusively locks path until the returned file is closed. The
// OS releases the lock if the process exits. The sidecar is never unlinked:
// replacing it would allow concurrent locks on different file identities.
func Acquire(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		locked, err := tryLock(f)
		if err != nil {
			f.Close()
			return nil, &os.PathError{Op: "lock", Path: path, Err: err}
		}
		if locked {
			return f, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
