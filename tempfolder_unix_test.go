//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// envHas reports whether env holds an entry of the name.
func envHas(env []string, name string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, name+"=") {
			return true
		}
	}
	return false
}

// countTempProbes counts the test files that are made in dir, through the seam. An
// empty dir counts every folder. hold, when not nil, runs inside the seam.
func countTempProbes(t *testing.T, dir string, hold func(path string)) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := afterTempProbeCreate
	afterTempProbeCreate = func(path string) {
		if dir != "" && filepath.Dir(path) != dir {
			return
		}
		n.Add(1)
		if hold != nil {
			hold(path)
		}
	}
	t.Cleanup(func() { afterTempProbeCreate = old })
	return &n
}

// isolateTempEnv gives each temp-folder name an empty value, so that a value of the
// host does not start a test. A test sets its own names after it.
func isolateTempEnv(t *testing.T) {
	t.Helper()
	for _, name := range append([]string{"TMP"}, tempFolderNames...) {
		t.Setenv(name, "")
	}
}

// probeFiles lists the entries of dir whose name starts like a test file.
func probeFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".claude-ssh-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestTempFolderThatTakesNoFileIsNotHandedOn(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a 0555 folder")
	}
	root := t.TempDir()
	ok := filepath.Join(root, "ok")
	sub := filepath.Join(root, "ro", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ok, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{sub, filepath.Dir(sub)} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Dir(sub), 0o755)
		_ = os.Chmod(sub, 0o755)
	})
	for _, name := range tempFolderNames {
		t.Setenv(name, sub)
	}
	t.Setenv("TMP", ok)
	buf := captureLogBuf(t)

	env := buildSpawnEnv(nil, false)
	for _, name := range tempFolderNames {
		if envHas(env, name) {
			t.Errorf("the child env holds %s, want it not handed on", name)
		}
		re := regexp.MustCompile(`(?m)^WARN  \[process\.Manager\] the inherited ` + name + `=` +
			regexp.QuoteMeta(sub) + ` is not handed on to a child: open ` +
			regexp.QuoteMeta(sub) + `/\.claude-ssh-[0-9]+: permission denied$`)
		if !re.MatchString(buf.String()) {
			t.Errorf("no log line for %s, log:\n%s", name, buf.String())
		}
	}
	if !envHas(env, "TMP") {
		t.Error("the child env lost TMP, want it kept")
	}
}

func TestTempFolderThatTakesAFileStaysAndKeepsNoFile(t *testing.T) {
	dir := t.TempDir()
	isolateTempEnv(t)
	t.Setenv("TMPDIR", dir)
	n := countTempProbes(t, dir, nil)
	env := buildSpawnEnv(nil, false)
	if !envHas(env, "TMPDIR") {
		t.Error("the child env lost TMPDIR, want it kept")
	}
	if n.Load() != 1 {
		t.Errorf("%d test files made in %s, want 1", n.Load(), dir)
	}
	if left := probeFiles(t, dir); len(left) != 0 {
		t.Errorf("the folder still holds %v, want the test file removed", left)
	}
}

func TestTempFolderRelativeOrMissingIsNotTested(t *testing.T) {
	root := t.TempDir()
	isolateTempEnv(t)
	missing := filepath.Join(root, "missing", "sub")
	// The relative folder exists from the working folder of the test.
	if err := os.MkdirAll(filepath.Join(root, "rel", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("TMPDIR", "rel/dir")
	t.Setenv("TEMP", missing)
	n := countTempProbes(t, "", nil)
	buf := captureLogBuf(t)
	env := buildSpawnEnv(nil, false)
	for _, name := range []string{"TMPDIR", "TEMP"} {
		if !envHas(env, name) {
			t.Errorf("the child env lost %s, want it kept", name)
		}
		if strings.Contains(buf.String(), "the inherited "+name+"=") {
			t.Errorf("a log line for %s, want none:\n%s", name, buf.String())
		}
	}
	if n.Load() != 0 {
		t.Error("a test file was made, want no test of a relative or missing folder")
	}
}

func TestTempFolderSpawnsAtOnceShareOneTest(t *testing.T) {
	dir := t.TempDir()
	isolateTempEnv(t)
	t.Setenv("TMPDIR", dir)
	release := make(chan struct{})
	n := countTempProbes(t, dir, func(string) { <-release })
	old := tempProbeWait
	tempProbeWait = time.Minute
	t.Cleanup(func() { tempProbeWait = old })

	var wg sync.WaitGroup
	kept := make([]bool, 3)
	for i := range kept {
		wg.Add(1)
		go func() {
			defer wg.Done()
			kept[i] = envHas(buildSpawnEnv(nil, false), "TMPDIR")
		}()
	}
	waiters := func() int {
		tempProbes.mu.Lock()
		defer tempProbes.mu.Unlock()
		if p := tempProbes.inFlight[dir]; p != nil {
			return p.waiters
		}
		return 0
	}
	deadline := time.Now().Add(10 * time.Second)
	for waiters() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := waiters()
	close(release)
	wg.Wait()
	if got != 3 {
		t.Fatalf("%d spawns wait on the test, want 3", got)
	}
	if n.Load() != 1 {
		t.Errorf("%d test files made, want 1 for three spawns at once", n.Load())
	}
	for i, k := range kept {
		if !k {
			t.Errorf("spawn %d lost TMPDIR, want it kept", i)
		}
	}
}

// waitTempProbeEnd releases the held test of dir and polls until it ends, so that
// the temp folder of the test is not removed under it.
func waitTempProbeEnd(t *testing.T, dir string, release chan struct{}) {
	t.Helper()
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tempProbes.mu.Lock()
		p := tempProbes.inFlight[dir]
		tempProbes.mu.Unlock()
		if p == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the held test did not end within 10 s")
}

func TestTempFolderWaitEndsAndKeepsTheName(t *testing.T) {
	dir := t.TempDir()
	isolateTempEnv(t)
	t.Setenv("TMPDIR", dir)
	release := make(chan struct{})
	n := countTempProbes(t, dir, func(string) { <-release })
	old := tempProbeWait
	tempProbeWait = 20 * time.Millisecond
	t.Cleanup(func() { tempProbeWait = old })
	defer waitTempProbeEnd(t, dir, release)

	done := make(chan bool, 1)
	go func() { done <- envHas(buildSpawnEnv(nil, false), "TMPDIR") }()
	select {
	case kept := <-done:
		if !kept {
			t.Error("the child env lost TMPDIR after the wait, want it kept")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the spawn still waits on a held test after 5 s")
	}
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n.Load() != 1 {
		t.Errorf("%d test files made, want 1", n.Load())
	}
}

func TestTempFolderOneWaitForAllNames(t *testing.T) {
	dir := t.TempDir()
	isolateTempEnv(t)
	for _, name := range tempFolderNames {
		t.Setenv(name, dir)
	}
	release := make(chan struct{})
	countTempProbes(t, dir, func(string) { <-release })
	old := tempProbeWait
	tempProbeWait = 500 * time.Millisecond
	t.Cleanup(func() { tempProbeWait = old })
	defer waitTempProbeEnd(t, dir, release)

	start := time.Now()
	env := buildSpawnEnv(nil, false)
	// One wait per name takes three times the limit.
	if took := time.Since(start); took >= 2*tempProbeWait {
		t.Errorf("the spawn waited %v for three names on one held folder, want at most about %v", took, tempProbeWait)
	}
	for _, name := range tempFolderNames {
		if !envHas(env, name) {
			t.Errorf("the child env lost %s after the wait, want it kept", name)
		}
	}
}

func TestTempFolderRemovesOnlyItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	isolateTempEnv(t)
	t.Setenv("TMPDIR", dir)
	before := filepath.Join(dir, ".claude-ssh-1")
	if err := os.WriteFile(before, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Between the create and the remove, another file takes the name.
	var swapped string
	n := countTempProbes(t, dir, func(path string) {
		swapped = path
		if err := os.Rename(path, path+".moved"); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(path, []byte("other"), 0o600); err != nil {
			t.Error(err)
		}
	})
	buildSpawnEnv(nil, false)
	if n.Load() != 1 {
		t.Fatalf("%d test files made, want 1", n.Load())
	}
	if _, err := os.Stat(before); err != nil {
		t.Errorf("the file that was there is gone: %v", err)
	}
	if b, err := os.ReadFile(swapped); err != nil || string(b) != "other" {
		t.Errorf("the file that took the name is gone or changed (%q, %v), want it kept", b, err)
	}
}

func TestTempFolderKeepsALinkAtTheName(t *testing.T) {
	dir := t.TempDir()
	isolateTempEnv(t)
	t.Setenv("TMPDIR", dir)
	// Between the create and the remove, a symbolic link to the moved file takes the
	// name. It names the same file, so only an Lstat tells the two apart.
	var link string
	n := countTempProbes(t, dir, func(path string) {
		link = path
		if err := os.Rename(path, path+".moved"); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(path+".moved", path); err != nil {
			t.Error(err)
		}
	})
	buildSpawnEnv(nil, false)
	if n.Load() != 1 {
		t.Fatalf("%d test files made, want 1", n.Load())
	}
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link at the name is gone or changed (%v), want it kept", err)
	}
}
