package platform

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "arcctl.lock")
	l, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("lock directory: %v, %v", st, err)
	}

	_, err = AcquireLock(path)
	var le *LockedError
	if !errors.As(err, &le) || !errors.Is(err, ErrLocked) {
		t.Fatalf("second AcquireLock = %v, want a LockedError", err)
	}
	if le.PID != os.Getpid() || le.Path != path {
		t.Fatalf("LockedError = %+v, want pid %d", le, os.Getpid())
	}

	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("second Release = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Release removed the lock file: %v", err)
	}
	l2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock after Release = %v", err)
	}
	l2.Release()
}

func TestLockedErrorText(t *testing.T) {
	tests := []struct {
		err  LockedError
		want string
	}{
		{LockedError{Path: "/d/arcctl.lock", PID: 42}, "another arcctl (pid 42) holds /d/arcctl.lock"},
		{LockedError{Path: "/d/arcctl.lock"}, "another arcctl holds /d/arcctl.lock"},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
	}
}

func TestHolderIgnoresJunk(t *testing.T) {
	dir := t.TempDir()
	for content, want := range map[string]int{"123\n": 123, "": 0, "abc": 0, "-5": 0, strings.Repeat("9", 40): 0} {
		p := filepath.Join(dir, "lock")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := holder(p); got != want {
			t.Errorf("holder(%q) = %d, want %d", content, got, want)
		}
	}
	if got := holder(filepath.Join(dir, "absent")); got != 0 {
		t.Errorf("holder(absent) = %d", got)
	}
}

const lockHelperEnv = "ARCCTL_TEST_LOCK_HELPER"

// TestLockHelper runs only as the child process of TestLockReleasedOnKill.
func TestLockHelper(t *testing.T) {
	path := os.Getenv(lockHelperEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	if _, err := AcquireLock(path); err != nil {
		os.Stdout.WriteString("error " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("held\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(0)
}

func TestLockReleasedOnKill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arcctl.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelper$")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "held\n" {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("helper said %q, %v", line, err)
	}

	_, err = AcquireLock(path)
	var le *LockedError
	if !errors.As(err, &le) || le.PID != cmd.Process.Pid {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("AcquireLock while the helper holds it = %v, want pid %d", err, cmd.Process.Pid)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	l, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock after the holder was killed = %v", err)
	}
	l.Release()
}
