package supervisor

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitOnPath puts a copy of this test binary named git first on PATH, so that a
// gitRead spawn starts a helper of the test.
func fakeGitOnPath(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestSpawnGitReadStepErrorReason pins the reason of a failed step: other, also
// for a text that ends like an errno that SpawnFailureReason reads
// (claustrum's own, not measured).
func TestSpawnGitReadStepErrorReason(t *testing.T) {
	fakeGitOnPath(t)
	m := newTestProcManager(t)
	text := "cannot write its copy of the repository's git config: mkdir /s/0123: permission denied"
	_, err := m.SpawnGitRead(nil, "gs", nil, t.TempDir(), nil, func([]string) ([]string, []string, func(), error) {
		return nil, nil, nil, errors.New(text)
	})
	if err == nil || err.Error() != text {
		t.Fatalf("SpawnGitRead error = %v, want %q", err, text)
	}
	if got := SpawnFailureReason(err, ""); got.Reason != "other" {
		t.Errorf("reason of a failed step = %q, want other", got.Reason)
	}
}

// TestSpawnGitReadReleaseAtExit pins the end of the hold of the copy: the release
// of the step runs when the wait on the child returns, not at its start.
func TestSpawnGitReadReleaseAtExit(t *testing.T) {
	fakeGitOnPath(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	var released atomic.Int32
	p, err := m.SpawnGitRead(c, "gh", nil, t.TempDir(), nil, func(env []string) ([]string, []string, func(), error) {
		return []string{"30"}, append(env, "CLAUSTRUM_TEST_HELPER=sleep"), func() { released.Add(1) }, nil
	})
	if err != nil {
		t.Fatalf("SpawnGitRead: %v", err)
	}
	if n := released.Load(); n != 0 {
		t.Fatalf("release ran %d times while the child runs, want 0", n)
	}
	endOwnChild(t, p)
	deadline := time.Now().Add(5 * time.Second)
	for released.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := released.Load(); n != 1 {
		t.Errorf("release ran %d times after the exit, want 1", n)
	}
}
