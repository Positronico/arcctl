package platform

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ErrLocked means another arcctl process holds the single-instance lock.
var ErrLocked = errors.New("platform: another arcctl holds the lock")

// LockedError names the lock holder when its lock file says who it is.
type LockedError struct {
	Path string
	PID  int // 0 when unknown
}

func (e *LockedError) Error() string {
	if e.PID == 0 {
		return fmt.Sprintf("another arcctl holds %s", e.Path)
	}
	return fmt.Sprintf("another arcctl (pid %d) holds %s", e.PID, e.Path)
}

func (e *LockedError) Unwrap() error { return ErrLocked }

// Lock is the single-instance lock. The OS releases it when the process exits,
// however it exits, so a crash never leaves a stale lock.
type Lock struct {
	mu sync.Mutex
	f  *os.File
}

// AcquireLock takes the lock at path without waiting, creating the file and its
// directory when needed. It returns a *LockedError when another process holds
// it. The file is never removed: removing it would let two processes lock two
// different files under the same name.
func AcquireLock(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := lockFile(path)
	if errors.Is(err, ErrLocked) {
		return nil, &LockedError{Path: path, PID: holder(path)}
	}
	if err != nil {
		return nil, fmt.Errorf("platform: lock %s: %w", path, err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f}, nil
}

// Release gives the lock up. It is safe to call more than once.
func (l *Lock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	_ = l.f.Truncate(0)
	err := l.f.Close()
	l.f = nil
	return err
}

func holder(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 32))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
