//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestShadowFolderModesAndLock pins the modes of the folder of one daemon and
// the lock that the daemon holds. On 5fd08069 git-shadow and the folder of the
// daemon have 0700, and the lock file has 0600 and 0 bytes (s3p2-oldgit section
// 5 and s3-linux cell A3, Linux VM). The daemon holds an exclusive flock on the
// file for its life (s3p2b-edges-linux2 cell 44: the FLOCK line of /proc/locks).
func TestShadowFolderModesAndLock(t *testing.T) {
	f := newShadowFixture(t)
	f.info(t, f.repo, nil)
	dir := daemonFolder(t, f.run)
	lock := filepath.Join(dir, shadowLockName)
	for path, want := range map[string]os.FileMode{
		filepath.Join(f.run, shadowRootName): os.ModeDir | 0o700,
		dir:                                  os.ModeDir | 0o700,
		lock:                                 0o600,
	} {
		fi, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode() != want {
			t.Errorf("%s has mode %v, want %v", path, fi.Mode(), want)
		}
	}
	second, err := os.OpenFile(lock, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	take := func() error { return syscall.Flock(int(second.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
	if err := take(); err != syscall.EWOULDBLOCK {
		t.Fatalf("a second holder of the lock: %v, want EWOULDBLOCK", err)
	}
	// A later request keeps the lock.
	f.info(t, f.repo, nil)
	if err := take(); err != syscall.EWOULDBLOCK {
		t.Fatalf("a second holder after one more request: %v, want EWOULDBLOCK", err)
	}
	f.s.gitShadow.close()
	if err := take(); err != nil {
		t.Errorf("the lock after the end of the daemon: %v, want it free", err)
	}
}

// TestShadowStartTestCalls pins the ten calls of the start test: the argv, the
// working directory and the variables of each, in the order of the call log of
// 5fd08069 (s3p2-linux cell B3a, Linux VM). The read of the user's excludes
// file comes before them, and the calls of the method after them.
func TestShadowStartTestCalls(t *testing.T) {
	f := newShadowFixture(t)
	raw, calls := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	if len(calls) < 12 || !calls[0].excludesRead() {
		t.Fatalf("%d calls, the first %v\nwant the excludes read, ten test calls and the calls of the method", len(calls), calls[0].argv)
	}
	dir := daemonFolder(t, f.run)
	check := calls[1].cwd
	if !regexp.MustCompile(`^` + regexp.QuoteMeta(dir) + `/\.check-[0-9]+$`).MatchString(check) {
		t.Fatalf("working directory of the first test call = %q, want %s/.check-<n>", check, dir)
	}
	realDir, shadowDir := check+"/real", check+"/shadow"
	id := calls[5].argv[len(calls[5].argv)-1]
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(id) {
		t.Fatalf("the id of update-ref = %q", id)
	}
	prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "commit.gpgsign=false"}
	nine := []string{
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C", "LANGUAGE=C", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=c", "GIT_AUTHOR_EMAIL=c@c", "GIT_COMMITTER_NAME=c", "GIT_COMMITTER_EMAIL=c@c",
	}
	real := []string{"GIT_DIR=" + realDir + "/.git", "GIT_WORK_TREE=" + realDir}
	shadow := append(slices.Clone(real), "GIT_COMMON_DIR="+shadowDir, "GIT_OBJECT_DIRECTORY="+realDir+"/.git/objects")
	want := []struct {
		args []string
		tail []string
	}{
		{[]string{"init", "-q", "--template=", realDir}, nil},
		{[]string{"config", "helper.check", "real"}, real},
		{[]string{"mktree"}, real},
		{[]string{"commit-tree", "-m", "c", "4b825dc642cb6eb9a060e54bf8d69288fbee4904"}, real},
		{[]string{"update-ref", "HEAD", id}, real},
		{[]string{"config", "--get", "helper.check"}, shadow},
		{nil, nil}, // call 7, below
		{[]string{"config", "--get", "helper.check"}, shadow},
		{[]string{"rev-parse", "--verify", "-q", "HEAD^{commit}"}, shadow},
		{[]string{"worktree", "add", "--no-checkout", "--detach", check + "/wt", id}, shadow},
	}
	folderNames := []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY"}
	for i, w := range want {
		c := calls[i+1]
		if w.args == nil {
			if !slices.Equal(c.argv, []string{"config", "--no-includes", "--file", "-", "--list", "-z"}) || c.cwd != "/" {
				t.Errorf("call 7 = %v in %s", c.argv, c.cwd)
			}
			if n := len(c.env); n < 3 || !slices.Equal(c.env[n-3:], []string{"GIT_DIR=/dev/null", "LC_ALL=C", "LANGUAGE=C"}) {
				t.Errorf("call 7: the last variables are %v", c.env[max(0, len(c.env)-3):])
			}
			for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMON_DIR", "GIT_WORK_TREE"} {
				if _, ok := lookupExact(c.env, name); ok {
					t.Errorf("call 7 has %s", name)
				}
			}
			continue
		}
		if !slices.Equal(c.argv, append(slices.Clone(prefix), w.args...)) {
			t.Errorf("call %d argv = %v\nwant the prefix and %v", i+1, c.argv, w.args)
		}
		if c.cwd != check {
			t.Errorf("call %d runs in %s, want %s", i+1, c.cwd, check)
		}
		if len(c.env) < len(nine)+len(w.tail) || !slices.Equal(c.env[:len(nine)], nine) {
			t.Errorf("call %d: the first variables are %v\nwant %v", i+1, c.env[:min(len(c.env), len(nine))], nine)
			continue
		}
		if got := c.env[len(c.env)-len(w.tail):]; !slices.Equal(got, w.tail) {
			t.Errorf("call %d: the last variables are %v\nwant %v", i+1, got, w.tail)
		}
		var folder int
		for _, kv := range c.env {
			if slices.Contains(folderNames, envName(kv)) {
				folder++
			}
		}
		if folder != len(w.tail) {
			t.Errorf("call %d has %d folder variables, want %d", i+1, folder, len(w.tail))
		}
	}
	// The calls of the method follow, and none of them names the test folder.
	if n := len(testCalls(calls[11:])); n != 0 {
		t.Errorf("%d calls after the test name its folder", n)
	}
	if got, want := entryNames(t, dir), []string{shadowRepoName(filepath.Join(f.repo, ".git")), shadowLockName}; !slices.Equal(got, want) {
		t.Errorf("folder of the daemon at the reply holds %v, want %v", got, want)
	}
}

// lowerShadowBound sets the bound of a test call and the wait for its pipes.
func lowerShadowBound(t *testing.T, bound, drain time.Duration) {
	t.Helper()
	oldBound, oldDrain := shadowCallBound, shadowCallDrain
	shadowCallBound, shadowCallDrain = bound, drain
	t.Cleanup(func() { shadowCallBound, shadowCallDrain = oldBound, oldDrain })
}

// TestShadowCallBound pins both sides of the bound of a test call, with a
// lowered bound. A call that the stub holds for less passes. An `init` that it
// holds for longer is ended: the cause is `signal: killed`, with the answer of
// `git version` in brackets after two version calls. A `rev-parse --verify`
// that is ended is followed by the second rev-parse, and the cause names the
// ended call (s3p2b-edges-linux2 cell 31, Linux VM: holds of 29 s and of 32 s).
func TestShadowCallBound(t *testing.T) {
	out, err := exec.Command("git", "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(out))

	t.Run("a held init under the bound passes", func(t *testing.T) {
		f := newShadowFixture(t)
		lowerShadowBound(t, 20*time.Second, time.Second)
		slowGit(t, "init", "pre", 300*time.Millisecond, "", "")
		if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
			t.Errorf("git.info = %s", raw)
		}
	})
	t.Run("a held init over the bound is ended", func(t *testing.T) {
		f := newShadowFixture(t)
		lowerShadowBound(t, 2*time.Second, 100*time.Millisecond)
		slowGit(t, "init", "pre", 60*time.Second, "", "")
		start := time.Now()
		raw, calls := f.info(t, f.repo, nil)
		if d := time.Since(start); d > 15*time.Second {
			t.Errorf("the reply came after %v, want it soon after the bound of 2 s", d)
		}
		dir := daemonFolder(t, f.run)
		want := jsonQuoted(t, failedTestText("git init failed under "+strconv.Quote(dir)+": signal: killed ("+version+")"))
		if !strings.Contains(raw, `"message":`+want) {
			t.Errorf("git.info = %s\nwant the message %s", raw, want)
		}
		// The excludes read, the init and the two version calls.
		if len(calls) != 4 {
			t.Fatalf("%d calls, want 4", len(calls))
		}
		plain, inTest := calls[2], calls[3]
		if !slices.Equal(plain.argv, []string{"version"}) || plain.cwd != "/" {
			t.Errorf("first version call = %v in %s", plain.argv, plain.cwd)
		}
		if _, ok := lookupExact(plain.env, "GIT_AUTHOR_NAME"); ok {
			t.Error("the first version call has the variables of the test")
		}
		if n := len(inTest.argv); n != 9 || inTest.argv[8] != "version" || inTest.argv[1] != "core.hooksPath=/dev/null" ||
			filepath.Dir(inTest.cwd) != dir || inTest.env[0] != "GIT_CONFIG_NOSYSTEM=1" {
			t.Errorf("second version call = %v in %s", inTest.argv, inTest.cwd)
		}
		if got := entryNames(t, dir); !slices.Equal(got, []string{shadowLockName}) {
			t.Errorf("folder of the daemon after the ended call holds %v", got)
		}
	})
	t.Run("a held rev-parse over the bound is ended", func(t *testing.T) {
		f := newShadowFixture(t)
		lowerShadowBound(t, 3*time.Second, 100*time.Millisecond)
		slowGit(t, "rev-parse,--verify", "pre", 60*time.Second, "", "")
		t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_COMMON_DIR=/shadow")
		raw, calls := f.info(t, f.repo, nil)
		want := jsonQuoted(t, failedTestText("git rev-parse failed under "+strconv.Quote(daemonFolder(t, f.run))+": signal: killed"))
		if !strings.Contains(raw, `"message":`+want) {
			t.Errorf("git.info = %s\nwant the message %s", raw, want)
		}
		last := calls[len(calls)-1]
		if _, shadow := lookupExact(last.env, "GIT_COMMON_DIR"); shadow || !slices.Contains(last.argv, "--verify") {
			t.Errorf("the last call = %v, want the second rev-parse without GIT_COMMON_DIR", last.argv)
		}
	})
}

// TestShadowEndedAddNeverGivesFallback pins the rule of a first test `worktree
// add` that the bound ended: the second add runs, and the test failed with the
// text of the ended call, also when the second add works. The state is not
// fallback-git, no state line is logged, and a request after the kept time
// tests again. On a Windows VM 5fd08069 answers so in 4 of 4 runs, with `exit
// status 1` as the text of the ended call (val-s3p2b2-1-windows cell
// Q-S4h-hold32-worktree-add). On a macOS VM the cause ends with `signal:
// killed` (val-s3p2b2-1-macos section S4), as here.
func TestShadowEndedAddNeverGivesFallback(t *testing.T) {
	f := newShadowFixture(t)
	now := useShadowClock(t)
	logs := captureLogBuf(t)
	lowerShadowBound(t, 3*time.Second, 100*time.Millisecond)
	slowGit(t, "worktree,add", "pre", 60*time.Second, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ARGEND", "/wt")
	raw, calls := f.info(t, f.repo, nil)
	want := jsonQuoted(t, failedTestText("git worktree failed under "+strconv.Quote(daemonFolder(t, f.run))+": signal: killed"))
	if !strings.Contains(raw, `"message":`+want) {
		t.Fatalf("git.info = %s\nwant the message %s", raw, want)
	}
	last := calls[len(calls)-1]
	if _, shadow := lookupExact(last.env, "GIT_COMMON_DIR"); shadow || !strings.HasSuffix(last.argv[len(last.argv)-2], "/wt2") {
		t.Errorf("the last call = %v, want the add of wt2 without GIT_COMMON_DIR", last.argv)
	}
	if n := stateLines(logs.String()); n != 0 {
		t.Errorf("%d state lines after the ended add, want none", n)
	}
	if again, calls := f.info(t, f.repo, nil); again != raw || len(calls) != 0 {
		t.Errorf("the next request = %s with %d calls, want the kept text and no call", again, len(calls))
	}
	// After the kept time the test runs again, and it passes with no held call.
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH", "")
	*now = now.Add(shadowFailureKeep)
	raw, calls = f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"settingsControl":"shadow"`) || len(testCalls(calls)) != 9 {
		t.Errorf("after the kept time: %s with %d test calls, want the state shadow after a new test", raw, len(testCalls(calls)))
	}
}

// TestShadowFolderFailures pins the cause where a folder or a file of the test
// cannot be made: the error of the system call with its path, and no quotes.
// 5fd08069 answers `mkdir <run>/git-shadow: permission denied` for a run folder
// with mode 0500, and `mkdir <S>: permission denied` for a git-shadow with that
// mode (s3p2b-edges-linux2 cells 30 and 32, Linux VM). Each request makes one
// git call, the read of the excludes file, and the text is kept.
func TestShadowFolderFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root passes a folder with mode 0500")
	}
	lock := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	}
	check := func(t *testing.T, f shadowFixture, cause *regexp.Regexp) {
		t.Helper()
		raw, calls := f.info(t, f.repo, nil)
		want := regexp.MustCompile(`^\{"jsonrpc":"2\.0","id":1,"error":\{"code":-32603,"message":"Claude could not test git on the remote machine \(` +
			cause.String() + `\)\. Check free disk space and quota there, then try again\."\}\}$`)
		if !want.MatchString(raw) {
			t.Errorf("git.info = %s\nwant %s", raw, want)
		}
		if len(calls) != 1 || !calls[0].excludesRead() {
			t.Errorf("%d calls, want only the read of the excludes file", len(calls))
		}
		if again, calls := f.info(t, f.repo, nil); again != raw || len(calls) != 0 {
			t.Errorf("again = %s with %d calls, want the kept text and no call", again, len(calls))
		}
	}
	name := `[^/"]+-` + strconv.Itoa(os.Getpid()) + `-[0-9a-f]{16}`

	t.Run("the run folder takes no folder", func(t *testing.T) {
		f := newShadowFixture(t)
		lock(t, f.run)
		check(t, f, regexp.MustCompile(`mkdir `+regexp.QuoteMeta(f.run)+`/git-shadow: permission denied`))
		if names := daemonFolders(t, f.run); names != nil {
			t.Errorf("git-shadow was made: %v", names)
		}
	})
	t.Run("git-shadow takes no folder", func(t *testing.T) {
		f := newShadowFixture(t)
		root := filepath.Join(f.run, shadowRootName)
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		lock(t, root)
		check(t, f, regexp.MustCompile(`mkdir `+regexp.QuoteMeta(root)+`/`+name+`: permission denied`))
		if names := daemonFolders(t, f.run); len(names) != 0 {
			t.Errorf("git-shadow holds %v", names)
		}
	})
	// The two rows below ran on a tmpfs with 2 and with 3 inodes on 5fd08069
	// (cell 32): `open <S>/lock: no space left on device` with no folder of the
	// daemon after it, and `mkdir <S>/.check-<n>: no space left on device` with
	// the folder and its lock file after it. Here the lock step fails through
	// its seam, and the folder of the daemon takes no entry after that step.
	t.Run("the lock file cannot be made", func(t *testing.T) {
		f := newShadowFixture(t)
		old := openShadowLock
		openShadowLock = func(path string) (*os.File, error) {
			return nil, &os.PathError{Op: "open", Path: path, Err: syscall.ENOSPC}
		}
		t.Cleanup(func() { openShadowLock = old })
		root := filepath.Join(f.run, shadowRootName)
		check(t, f, regexp.MustCompile(`open `+regexp.QuoteMeta(root)+`/`+name+`/lock: no space left on device`))
		if names := daemonFolders(t, f.run); names == nil || len(names) != 0 {
			t.Errorf("git-shadow holds %v, want an empty folder", names)
		}
	})
	t.Run("the folder of the test cannot be made", func(t *testing.T) {
		f := newShadowFixture(t)
		old := openShadowLock
		openShadowLock = func(path string) (*os.File, error) {
			file, err := old(path)
			if err == nil {
				lock(t, filepath.Dir(path))
			}
			return file, err
		}
		t.Cleanup(func() { openShadowLock = old })
		root := filepath.Join(f.run, shadowRootName)
		check(t, f, regexp.MustCompile(`mkdir `+regexp.QuoteMeta(root)+`/`+name+`/\.check-[0-9]+: permission denied`))
		if got := entryNames(t, daemonFolder(t, f.run)); !slices.Equal(got, []string{shadowLockName}) {
			t.Errorf("folder of the daemon holds %v, want only the lock file", got)
		}
	})
}

// TestServeDaemonRemovesItsShadowFolder pins the wiring of the -serve daemon:
// its settings control has the folder of the socket as its run folder, and the
// teardown removes the folder of the daemon and leaves the empty git-shadow.
// 5fd08069 does so at -stop and at server.shutdown (s3p2-linux section 1, Linux
// VM).
func TestServeDaemonRemovesItsShadowFolder(t *testing.T) {
	f := newShadowFixture(t)
	run := resolveTestRoot(t, shortTempDir(t))
	sock := filepath.Join(run, "s.sock")
	s, err := newServerOnSocket(sock, "tok", "", wireLogOptions{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.procs.killAll() })
	t.Cleanup(func() { _ = s.ln.Close() })
	line := strings.Replace(rpcLine(t, "git.info", map[string]any{"path": f.repo}), testToken, "tok", 1)
	if raw := dispatchRaw(t, s, line); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, run)
	s.signalShutdown()
	s.closeAll(sock)
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder of the daemon after the teardown: %v, want it gone", err)
	}
	fi, err := os.Lstat(filepath.Join(run, shadowRootName))
	if err != nil || fi.Mode() != os.ModeDir|0o700 {
		t.Fatalf("git-shadow after the teardown: %v, %v, want an empty folder with 0700", fi, err)
	}
	if names := daemonFolders(t, run); len(names) != 0 {
		t.Errorf("git-shadow after the teardown holds %v", names)
	}
}
