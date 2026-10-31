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
	for _, name := range tempFolderNames {
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
	sub := filepath.Join(t.TempDir(), "ro", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
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
	dropped := []string{"CLAUDE_CODE_TMPDIR", "TMPDIR", "TMP", "TEMP"}
	for _, name := range dropped {
		t.Setenv(name, sub)
	}
	buf := captureLogBuf(t)

	env := buildSpawnEnv(nil, false)
	at := -1
	for _, name := range dropped {
		if envHas(env, name) {
			t.Errorf("the child env holds %s, want it not handed on", name)
		}
		re := regexp.MustCompile(`(?m)^WARN  \[process\.Manager\] the inherited ` + name + `=` +
			regexp.QuoteMeta(sub) + ` is not handed on to a child: open ` +
			regexp.QuoteMeta(sub) + `/\.claude-ssh-[0-9]+: permission denied$`)
		loc := re.FindStringIndex(buf.String())
		if loc == nil {
			t.Errorf("no log line for %s, log:\n%s", name, buf.String())
			continue
		}
		// The lines come in the order of the names.
		if loc[0] < at {
			t.Errorf("the log line for %s comes before the line of the name ahead of it, log:\n%s", name, buf.String())
		}
		at = loc[0]
	}
}

func TestTempFolderMissingUnderAFolderThatTakesNoFileIsNotHandedOn(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a 0555 folder")
	}
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	isolateTempEnv(t)
	sub := filepath.Join(ro, "sub")
	t.Setenv("TMPDIR", sub)
	buf := captureLogBuf(t)

	env := buildSpawnEnv(nil, false)
	if envHas(env, "TMPDIR") {
		t.Error("the child env holds TMPDIR, want it not handed on")
	}
	// The create in the nearest existing folder fails, and the line names that file.
	re := regexp.MustCompile(`(?m)^WARN  \[process\.Manager\] the inherited TMPDIR=` +
		regexp.QuoteMeta(sub) + ` is not handed on to a child: open ` +
		regexp.QuoteMeta(ro) + `/\.claude-ssh-[0-9]+: permission denied$`)
	if !re.MatchString(buf.String()) {
		t.Errorf("no log line that names the file in %s, log:\n%s", ro, buf.String())
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

func TestTempFolderRelativeIsNotTested(t *testing.T) {
	root := t.TempDir()
	isolateTempEnv(t)
	// The relative folder exists from the working folder of the test.
	if err := os.MkdirAll(filepath.Join(root, "rel", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("TMPDIR", "rel/dir")
	n := countTempProbes(t, "", nil)
	buf := captureLogBuf(t)
	env := buildSpawnEnv(nil, false)
	if !envHas(env, "TMPDIR") {
		t.Error("the child env lost TMPDIR, want it kept")
	}
	if strings.Contains(buf.String(), "the inherited TMPDIR=rel/dir") {
		t.Errorf("a log line for TMPDIR, want none:\n%s", buf.String())
	}
	if n.Load() != 0 {
		t.Error("a test file was made, want no test of a relative folder")
	}
}

func TestTempFolderMissingIsTestedInTheNearestFolder(t *testing.T) {
	w := filepath.Join(t.TempDir(), "w")
	if err := os.Mkdir(w, 0o755); err != nil {
		t.Fatal(err)
	}
	isolateTempEnv(t)
	missing := filepath.Join(w, "miss", "sub")
	t.Setenv("TEMP", missing)
	n := countTempProbes(t, w, nil)
	buf := captureLogBuf(t)
	env := buildSpawnEnv(nil, false)
	if !envHas(env, "TEMP") {
		t.Error("the child env lost TEMP, want it kept")
	}
	if strings.Contains(buf.String(), "the inherited TEMP="+missing) {
		t.Errorf("a log line for TEMP, want none:\n%s", buf.String())
	}
	if n.Load() != 1 {
		t.Errorf("%d test files made in %s, want 1", n.Load(), w)
	}
	if left := probeFiles(t, w); len(left) != 0 {
		t.Errorf("the folder still holds %v, want the test file removed", left)
	}
	if _, err := os.Lstat(filepath.Join(w, "miss")); err == nil {
		t.Error("the missing folder exists after the test, want nothing made")
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
	// One wait per name takes four times the limit.
	if took := time.Since(start); took >= 2*tempProbeWait {
		t.Errorf("the spawn waited %v for four names on one held folder, want at most about %v", took, tempProbeWait)
	}
	for _, name := range tempFolderNames {
		if !envHas(env, name) {
			t.Errorf("the child env lost %s after the wait, want it kept", name)
		}
	}
}

func TestTempFolderSlowTestLogsOnceAndALateSpawnDoesNotWait(t *testing.T) {
	dir := t.TempDir()
	isolateTempEnv(t)
	t.Setenv("TMPDIR", dir)
	release := make(chan struct{})
	countTempProbes(t, dir, func(string) { <-release })
	old := tempProbeWait
	tempProbeWait = 300 * time.Millisecond
	t.Cleanup(func() { tempProbeWait = old })
	defer waitTempProbeEnd(t, dir, release)
	buf := captureLogBuf(t)
	line := "WARN  [process.Manager] the temporary folder " + dir + " has not answered in " +
		tempProbeWait.String() + ": children keep it until it does\n"

	if !envHas(buildSpawnEnv(nil, false), "TMPDIR") {
		t.Error("the first spawn lost TMPDIR after the wait, want it kept")
	}
	if got := strings.Count(buf.String(), line); got != 1 {
		t.Errorf("%d lines %q after the first spawn, want 1, log:\n%s", got, line, buf.String())
	}
	// The test still runs and is older than tempProbeWait. A spawn that joins it
	// does not wait and logs nothing.
	start := time.Now()
	if !envHas(buildSpawnEnv(nil, false), "TMPDIR") {
		t.Error("the late spawn lost TMPDIR, want it kept")
	}
	if took := time.Since(start); took >= tempProbeWait/2 {
		t.Errorf("the late spawn waited %v on a test older than %v, want no wait", took, tempProbeWait)
	}
	if got := strings.Count(buf.String(), line); got != 1 {
		t.Errorf("%d lines %q after the late spawn, want 1, log:\n%s", got, line, buf.String())
	}
}

func TestTempFolderAnsweredTestCountsAfterAnotherWaitRanOut(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a 0555 folder")
	}
	root := t.TempDir()
	held := filepath.Join(root, "held")
	ro := filepath.Join(root, "ro")
	if err := os.Mkdir(held, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	isolateTempEnv(t)
	// The test of the first name hangs past the wait. The test of the second name
	// fails at once, and its answer counts after the first wait ran out.
	t.Setenv("CLAUDE_CODE_TMPDIR", held)
	t.Setenv("TMPDIR", ro)
	release := make(chan struct{})
	countTempProbes(t, held, func(string) { <-release })
	old := tempProbeWait
	tempProbeWait = 200 * time.Millisecond
	t.Cleanup(func() { tempProbeWait = old })
	defer waitTempProbeEnd(t, held, release)
	buf := captureLogBuf(t)

	env := buildSpawnEnv(nil, false)
	if !envHas(env, "CLAUDE_CODE_TMPDIR") {
		t.Error("the child env lost CLAUDE_CODE_TMPDIR after the wait, want it kept")
	}
	if envHas(env, "TMPDIR") {
		t.Error("the child env holds TMPDIR, want it not handed on")
	}
	if !strings.Contains(buf.String(), "the inherited TMPDIR="+ro+" is not handed on") {
		t.Errorf("no drop line for TMPDIR, log:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "the temporary folder "+ro+" ") {
		t.Errorf("a slow line for %s, want none, log:\n%s", ro, buf.String())
	}
}

// A symbolic link to nothing as the folder ends the test with the error of the
// create, because the Lstat finds the link. The parent takes no test (claustrum's own,
// not measured).
func TestTempFolderDanglingLinkIsNotHandedOn(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "gone"), link); err != nil {
		t.Fatal(err)
	}
	isolateTempEnv(t)
	t.Setenv("TMPDIR", link)
	n := countTempProbes(t, "", nil)
	buf := captureLogBuf(t)

	env := buildSpawnEnv(nil, false)
	if envHas(env, "TMPDIR") {
		t.Error("the child env holds TMPDIR, want it not handed on")
	}
	re := regexp.MustCompile(`(?m)^WARN  \[process\.Manager\] the inherited TMPDIR=` +
		regexp.QuoteMeta(link) + ` is not handed on to a child: open ` +
		regexp.QuoteMeta(link) + `/\.claude-ssh-[0-9]+: no such file or directory$`)
	if !re.MatchString(buf.String()) {
		t.Errorf("no log line with the error of the create, log:\n%s", buf.String())
	}
	if n.Load() != 0 {
		t.Errorf("%d test files made, want none in the parent of the link", n.Load())
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
