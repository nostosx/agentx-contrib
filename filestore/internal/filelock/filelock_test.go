package filelock_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nostosx/agentx-contrib/filestore/internal/filelock"
)

func TestLockExcludesOtherHandlesAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, testLockExcludesOtherHandlesAndHonorsCancellation)
}

func testLockExcludesOtherHandlesAndHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.lock")
	first, err := filelock.Acquire(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if second, err := filelock.Acquire(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("contending handle = %v, want context deadline", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := filelock.Acquire(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock sidecar was removed: %v", err)
	}
}

func TestProcessExitReleasesLock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "memory.lock")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestLockProcess$")
	cmd.Env = append(os.Environ(), "AGENTX_LOCK_TEST_PATH="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdout.Close() })
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("child did not acquire lock: %q, %v", line, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if f, err := filelock.Acquire(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("process lock did not exclude parent: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	f, err := filelock.Acquire(t.Context(), path)
	if err != nil {
		t.Fatalf("process exit retained lock: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLockProcess(t *testing.T) {
	path := os.Getenv("AGENTX_LOCK_TEST_PATH")
	if path == "" {
		t.Skip("subprocess helper")
	}
	f, err := filelock.Acquire(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	// Exit without closing the lock descriptor, as after an abrupt shutdown.
	runtime.KeepAlive(f)
	os.Exit(0)
}
