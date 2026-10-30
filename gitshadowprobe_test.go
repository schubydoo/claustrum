package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// isProbe reports the call that asks git about the repository: its common
// directory is the folder `.probe`.
func (c envCall) isProbe() bool {
	v, _ := lookupExact(c.env, "GIT_COMMON_DIR")
	return filepath.Base(v) == shadowProbeName
}

// plainVersion reports the `git version` call that follows a failure
// (versionAfterFailure). It has no option, and gitVersionDir is its working
// directory. It has none of the variables that the daemon adds to its other
// calls. A name that the test process holds itself does not count as added.
func (c envCall) plainVersion(t *testing.T) bool {
	t.Helper()
	if !slices.Equal(c.argv, []string{"version"}) {
		return false
	}
	if !sameFolder(c.cwd, gitVersionDir) {
		return false
	}
	for _, kv := range c.env {
		name := envName(kv)
		if _, own := os.LookupEnv(name); own {
			continue
		}
		switch name {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_WORK_TREE", "GIT_ALLOW_PROTOCOL",
			"GIT_TERMINAL_PROMPT", "GIT_NO_LAZY_FETCH", "GIT_CEILING_DIRECTORIES", "LC_ALL", "LANGUAGE":
			return false
		}
	}
	return true
}

func probeCalls(calls []envCall) []envCall {
	var out []envCall
	for _, c := range calls {
		if c.isProbe() {
			out = append(out, c)
		}
	}
	return out
}

// answerProbe makes the stub answer the call itself, with the exit code and
// the two texts. Every other call goes to the real git.
func answerProbe(t *testing.T, exit int, stdout, stderr string) {
	t.Helper()
	slowGit(t, "rev-parse,--git-dir", "fail", 0, stderr, stdout)
	t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_COMMON_DIR="+string(os.PathSeparator)+shadowProbeName)
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", strconv.Itoa(exit))
}

// answerSecondProbe makes the stub answer the second run of the call (the one
// with GIT_DIR=<.probe>) itself, with the exit code and the error text. The
// first call keeps the rule of answerProbe.
func answerSecondProbe(t *testing.T, exit int, stderr string) {
	t.Helper()
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "rev-parse,--git-dir")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVEND2", "GIT_DIR="+string(os.PathSeparator)+shadowProbeName)
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", strconv.Itoa(exit))
	t.Setenv("CLAUSTRUM_GITSTUB_STDERR2", stderr)
}

// setProbeClock gives the kept answer a clock that the test moves.
func setProbeClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Now()
	old := probeClock
	probeClock = func() time.Time { return now }
	t.Cleanup(func() { probeClock = old })
	return &now
}

// TestProbeFolder: after the start test gave the state shadow, the folder of
// the daemon holds `.probe` with HEAD (25 bytes), config (36 bytes) and the
// empty folders info, objects and refs (s3-linux cell A3, Linux VM. s3-macos
// cell A3. s3p2-windows cell B3). A folder of the daemon that is made again has
// none (s3-linux cell B6).
func TestProbeFolder(t *testing.T) {
	f := newShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, f.run)
	if got, want := entryNames(t, dir), []string{shadowProbeName, shadowRepoName(f.gitDir), shadowLockName}; !slices.Equal(got, want) {
		t.Fatalf("the folder of the daemon holds %v, want %v", got, want)
	}
	probe := filepath.Join(dir, shadowProbeName)
	if got, want := entryNames(t, probe), []string{"HEAD", "config", "info", "objects", "refs"}; !slices.Equal(got, want) {
		t.Fatalf(".probe holds %v, want %v", got, want)
	}
	head, config := mustRead(t, filepath.Join(probe, "HEAD")), mustRead(t, filepath.Join(probe, "config"))
	if head != "ref: refs/heads/.invalid\n" || len(head) != 25 {
		t.Errorf(".probe/HEAD = %q", head)
	}
	if config != "[core]\n\trepositoryformatversion = 0\n" || len(config) != 36 {
		t.Errorf(".probe/config = %q", config)
	}
	for _, name := range []string{"info", "objects", "refs"} {
		if got := entryNames(t, filepath.Join(probe, name)); len(got) != 0 {
			t.Errorf(".probe/%s holds %v, want nothing", name, got)
		}
	}
	if runtime.GOOS != "windows" {
		for name, want := range map[string]os.FileMode{".": 0o700, "HEAD": 0o600, "config": 0o600, "info": 0o700, "objects": 0o700, "refs": 0o700} {
			if fi, err := os.Lstat(filepath.Join(probe, name)); err != nil || fi.Mode().Perm() != want {
				t.Errorf(".probe/%s: %v, %v, want mode %o", name, fi, err, want)
			}
		}
	}
	// The folder of the daemon goes. A request inside the kept time of the
	// answer makes it again with no `.probe` and no call. The first request
	// after that time makes the call, and the folder with it.
	// The handle of the lock file is closed first, so that the remove works on
	// Windows too.
	now := setProbeClock(t)
	f.s.gitShadow.mu.Lock()
	f.s.gitShadow.dropLock()
	f.s.gitShadow.mu.Unlock()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	raw, calls := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"isRepo":true`) || len(probeCalls(calls)) != 0 {
		t.Fatalf("git.info after the folder went = %s with %d calls about the repository, want none", raw, len(probeCalls(calls)))
	}
	second := daemonFolder(t, f.run)
	if got, want := entryNames(t, second), []string{shadowRepoName(f.gitDir), shadowLockName}; !slices.Equal(got, want) {
		t.Errorf("the second folder of the daemon holds %v, want %v", got, want)
	}
	*now = now.Add(probeKeep)
	raw, calls = f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"isRepo":true`) || len(probeCalls(calls)) != 1 {
		t.Fatalf("git.info after the kept time = %s with %d calls about the repository, want one", raw, len(probeCalls(calls)))
	}
	if got, want := entryNames(t, second), []string{shadowProbeName, shadowRepoName(f.gitDir), shadowLockName}; !slices.Equal(got, want) {
		t.Errorf("after the call the second folder holds %v, want %v", got, want)
	}
}

// An emptied config of the folder `.probe` is written again at
// the next call about the repository. 5fd08069 has the 36 bytes again after a
// git.info 6 s after the file was emptied (cell C10 of round tier0-5, Linux
// VM). HEAD stays the file that it was.
func TestProbeConfigIsWrittenAgain(t *testing.T) {
	f := newShadowFixture(t)
	now := setProbeClock(t)
	f.info(t, f.repo, nil)
	probe := filepath.Join(daemonFolder(t, f.run), shadowProbeName)
	config, head := filepath.Join(probe, "config"), filepath.Join(probe, "HEAD")
	before, err := os.Stat(head)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(probeKeep + time.Second)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Errorf("git.info = %s", raw)
	}
	if got, err := os.ReadFile(config); err != nil || string(got) != probeConfigText || len(got) != 36 {
		t.Errorf("config after the request = %q, %v\nwant the 36 bytes %q", got, err, probeConfigText)
	}
	if after, err := os.Stat(head); err != nil || !os.SameFile(before, after) {
		t.Errorf("HEAD after the request: %v, want the same file", err)
	}
}

// TestProbeFolderIsWholeBeforeTheCall: a request never runs its call about the
// repository on a folder `.probe` that is still in the make. The test holds the
// lock of the make with only the top folder on the disk, as a first request
// does between its first two steps. A second request must wait. After the lock
// goes it finds no HEAD, makes the rest, and answers for the repository.
// Without the wait git answers "not a git repository" there and git.info says
// isRepo false for a good repository.
func TestProbeFolderIsWholeBeforeTheCall(t *testing.T) {
	f := newShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	now := setProbeClock(t)
	*now = now.Add(probeKeep)
	probe := filepath.Join(daemonFolder(t, f.run), shadowProbeName)
	if err := os.RemoveAll(probe); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(probe, 0o700); err != nil {
		t.Fatal(err)
	}
	h := f.s.gitShadow
	h.probeFolderMu.Lock()
	type reply struct {
		raw   string
		calls []envCall
	}
	done := make(chan reply, 1)
	go func() {
		raw, calls := f.info(t, f.repo, nil)
		done <- reply{raw, calls}
	}()
	select {
	case r := <-done:
		h.probeFolderMu.Unlock()
		t.Fatalf("git.info did not wait for the make of .probe: %s", r.raw)
	case <-time.After(300 * time.Millisecond):
	}
	h.probeFolderMu.Unlock()
	r := <-done
	if !strings.Contains(r.raw, `"isRepo":true`) || len(probeCalls(r.calls)) != 1 {
		t.Fatalf("git.info = %s with %d calls about the repository, want isRepo true and one call", r.raw, len(probeCalls(r.calls)))
	}
	if got, want := entryNames(t, probe), []string{"HEAD", "config", "info", "objects", "refs"}; !slices.Equal(got, want) {
		t.Errorf(".probe holds %v, want %v", got, want)
	}
}

// TestProbeCallShape pins the call and its place in a first request. The argv,
// the eight variables and their order are those of the call log of 5fd08069
// (s3p2-linux cell B3a and val-s3p2b1-linux cell A1, Linux VM). The call comes
// after the read of the excludes file and before the read of the config
// (s3p2-linux cell B3a, and cell 1 of s3p2b-edges2-linux). Its working
// directory is the root of the work tree and its ceiling the parent of that
// root, per kind of path (s3p2-linux cells B3a, B3b and B3c, s3p2-linux2 cell
// D7b). On Windows Go sorts the environment, so the order is not compared
// there.
func TestProbeCallShape(t *testing.T) {
	for _, row := range []struct {
		name string
		// path and root pick the request path and the wanted working directory
		// from the fixture.
		path, root func(t *testing.T, f shadowFixture) string
	}{
		{"the root of a repository", func(_ *testing.T, f shadowFixture) string { return f.repo }, func(_ *testing.T, f shadowFixture) string { return f.repo }},
		{"a linked worktree", func(_ *testing.T, f shadowFixture) string { return f.wt }, func(_ *testing.T, f shadowFixture) string { return f.wt }},
		{"a folder inside the work tree", func(t *testing.T, f shadowFixture) string {
			sub := filepath.Join(f.repo, "sub", "dir")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			return sub
		}, func(_ *testing.T, f shadowFixture) string { return f.repo }},
		{"a bare repository", func(t *testing.T, f shadowFixture) string {
			bare := filepath.Join(filepath.Dir(f.repo), "bare.git")
			runGit(t, filepath.Dir(f.repo), "init", "-q", "--bare", "-b", "main", bare)
			return bare
		}, func(_ *testing.T, f shadowFixture) string { return filepath.Join(filepath.Dir(f.repo), "bare.git") }},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			path, root := row.path(t, f), row.root(t, f)
			raw, calls := f.info(t, path, nil)
			if !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			if len(calls) < 4 || !calls[0].excludesRead() || !calls[1].isProbe() || !calls[2].configRead() || !calls[3].listing() {
				t.Fatalf("the first calls are %q, %q, %q, %q\nwant the excludes read, the call about the repository, the config read and the first listing",
					calls[0].argv, calls[1].argv, calls[2].argv, calls[3].argv)
			}
			c := calls[1]
			if want := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=", "rev-parse", "--git-dir"}; !slices.Equal(c.argv, want) {
				t.Errorf("argv = %q\nwant %q", c.argv, want)
			}
			if c.cwd != root {
				t.Errorf("the working directory = %s, want %s", c.cwd, root)
			}
			probe := filepath.Join(daemonFolder(t, f.run), shadowProbeName)
			want := []string{
				"GIT_COMMON_DIR=" + probe,
				"GIT_OBJECT_DIRECTORY=" + filepath.Join(probe, "objects"),
				"GIT_CEILING_DIRECTORIES=" + filepath.Dir(root),
				"GIT_ALLOW_PROTOCOL=denied_by_claude_ssh",
				"GIT_NO_LAZY_FETCH=1",
				"GIT_TERMINAL_PROMPT=0",
				"LC_ALL=C",
				"LANGUAGE=C",
			}
			names := append(slices.Clone(shapeProbeNames), "GIT_DIR", "GIT_WORK_TREE", "GIT_ASKPASS", "GIT_CONFIG_COUNT")
			var got []string
			for _, kv := range c.env {
				if slices.Contains(names, envName(kv)) {
					got = append(got, kv)
				}
			}
			if runtime.GOOS == "windows" {
				slices.Sort(got)
				slices.Sort(want)
			}
			if !slices.Equal(got, want) {
				t.Errorf("the variables = %q\nwant %q", got, want)
			}
		})
	}
}

// shapeProbeNames are the names of the eight variables of the call.
var shapeProbeNames = []string{
	"GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_CEILING_DIRECTORIES",
	"GIT_ALLOW_PROTOCOL", "GIT_NO_LAZY_FETCH", "GIT_TERMINAL_PROMPT", "LC_ALL", "LANGUAGE",
}

// TestProbeCeilingReplacesTheDaemonValue: the call has its own
// GIT_CEILING_DIRECTORIES, also when the daemon has one (s3p2-linux2 cell D5,
// Linux VM: daemon `<W>/home/repo`, this call `<W>/home`).
func TestProbeCeilingReplacesTheDaemonValue(t *testing.T) {
	f := newSeededShadowFixture(t)
	t.Setenv("GIT_CEILING_DIRECTORIES", f.repo)
	_, calls := f.info(t, f.repo, nil)
	probes := probeCalls(calls)
	if len(probes) != 1 {
		t.Fatalf("%d calls about the repository, want 1", len(probes))
	}
	var got []string
	for _, kv := range probes[0].env {
		if envName(kv) == "GIT_CEILING_DIRECTORIES" {
			got = append(got, kv)
		}
	}
	if want := []string{"GIT_CEILING_DIRECTORIES=" + filepath.Dir(f.repo)}; !slices.Equal(got, want) {
		t.Errorf("the ceiling of the call = %q, want %q", got, want)
	}
}

// TestProbeDropsFourNamesAndKeepsTheConfigCount: the call has none of the four
// names of probeDropNames and keeps the daemon's GIT_CONFIG_COUNT and its pair.
// The listing of the same request keeps all of them (cells C8, C8b, C8c and C8K
// of round tier3-02, Linux, macOS and Windows VMs).
func TestProbeDropsFourNamesAndKeepsTheConfigCount(t *testing.T) {
	f := newSeededShadowFixture(t)
	alt := t.TempDir()
	set := []string{
		"GIT_NAMESPACE=ns", "GIT_INDEX_FILE=" + filepath.Join(t.TempDir(), "index"),
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=1", "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + alt,
	}
	count := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=claustrum.probetest", "GIT_CONFIG_VALUE_0=1"}
	for _, kv := range append(slices.Clone(set), count...) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	_, calls := f.info(t, f.repo, nil)
	probes := probeCalls(calls)
	if len(probes) != 1 {
		t.Fatalf("%d calls about the repository, want 1", len(probes))
	}
	for _, kv := range set {
		if v, ok := lookupExact(probes[0].env, envName(kv)); ok {
			t.Errorf("the call keeps %s=%q", envName(kv), v)
		}
	}
	for _, kv := range count {
		if !slices.Contains(probes[0].env, kv) {
			t.Errorf("the call lost %s", kv)
		}
	}
	i := slices.IndexFunc(calls, envCall.listing)
	if i < 0 {
		t.Fatal("no listing")
	}
	for _, kv := range append(slices.Clone(set), count...) {
		if !slices.Contains(calls[i].env, kv) {
			t.Errorf("the listing lost %s", kv)
		}
	}
}

// probeErrorShapes are the reply of each method of fiveRequests to the error
// text of the call, in that order (cells 38 and 39 of s3p2b-edges-linux2, Linux
// VM): the error object on git.info, git.status and git.list_branches, the
// result object with worktree_add_failed on a create, and the lock-check text
// on a remove.
func probeErrorShapes(t *testing.T, f shadowFixture, text string) []string {
	t.Helper()
	q := strings.Trim(jsonQuoted(t, text), `"`)
	object := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"` + q + `"}}`
	return []string{object, object, object,
		`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` + q + `","errorCode":"worktree_add_failed"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"failed to remove worktree: could not check whether ` +
			strings.Trim(jsonQuoted(t, f.wt), `"`) + ` is locked: ` + q + `"}}`,
	}
}

// TestProbeAnswers pins what each answer of git leads to, per method. The rows
// are the wrapper rows of cell 39 of s3p2b-edges-linux2 (Linux VM), where
// 5fd08069 got each answer from a wrapper, and the owner row of that cell with
// the text of git for a repository of another owner. A bad answer makes no
// folder of the repository and no read of its config (the same cell). The
// counts are the calls about the repository per request: one, two on a remove,
// and twice that for an exit 128 or an answer that names another path.
func TestProbeAnswers(t *testing.T) {
	type answer struct {
		name           string
		exit           int
		stdout, stderr string
		// text is the error text for the repository root %[1]s and its git
		// directory %[2]s, or "" for the "no repository" answers.
		text  string
		calls int
		// version is true when one plain `git version` call follows the last
		// call about the repository of each answer. On 5fd08069 the requests
		// then have 13, 2, 2, 2 and 4 calls (cell 39 of val-s3p2b2-5-linux2,
		// rows exit 1 and exit 128, Linux VM).
		version bool
		// secondOK makes the second run of an exit 128 exit 0.
		secondOK bool
	}
	// The other path is absolute on the system of the test. Git prints a path
	// with forward slashes on Windows too, and the text shows it as printed.
	// On 5fd08069 the answer `C:/o/w/other/.git` gives `git finds
	// "C:/o/w/other/.git"`, and the answer with backslashes gives that path
	// with its backslashes (table P2 of val-s3p2b2-7-windows, Windows VM). No
	// test of the Windows rows ran on the host of the build.
	elsewhere := "/elsewhere/.git"
	var windowsRows []answer
	if runtime.GOOS == "windows" {
		elsewhere = "C:/elsewhere/.git"
		windowsRows = []answer{{name: "another path, backslashes", stdout: `C:\elsewhere\.git` + "\n", calls: 2,
			text: "git and Claude disagree about which repository holds this folder: from %[1]q git finds " +
				strconv.Quote(`C:\elsewhere\.git`) + ", Claude finds %[2]q; try again"}}
	}
	for _, a := range append([]answer{
		{name: "exit 1, no text", exit: 1, calls: 1, version: true,
			text: "git could not check this repository on the remote machine: at %[1]q: exit status 1"},
		// Exit 1 with a text: the text of git stands in the place of the exec
		// error (cells p3 of val-s3p2b2-7-linux2, Linux VM, 13 and 2 calls).
		{name: "p3: exit 1 with a text", exit: 1, calls: 1, version: true, stderr: "fatal: text of the wrapper for p3\n",
			text: "git could not check this repository on the remote machine: at %[1]q: fatal: text of the wrapper for p3"},
		// Two lines stand in the reply with one space between them, and exit 2
		// with a text has the rule of exit 1 (cell N-f of val-s3p2b2-8-linux2,
		// Linux VM, 13 and 2 calls). With the last line alone the first row
		// starts with `hint:`.
		{name: "N-f: exit 1 with two lines", exit: 1, calls: 1, version: true,
			stderr: "fatal: line one of the wrapper for p3\nhint: line two of the wrapper for p3\n",
			text:   "git could not check this repository on the remote machine: at %[1]q: fatal: line one of the wrapper for p3 hint: line two of the wrapper for p3"},
		{name: "N-f: exit 2 with a text", exit: 2, calls: 1, version: true, stderr: "fatal: text of the wrapper for p3 with exit 2\n",
			text: "git could not check this repository on the remote machine: at %[1]q: fatal: text of the wrapper for p3 with exit 2"},
		// Exit 128 with any text gets the second run of c8e2f083 (measured on
		// Linux and Windows). With secondOK it exits 0, and the answer is "no
		// repository". Else it fails as the first call did. The answer is then the
		// error with the text of the first call, after one `git version` call (not
		// measured for an exit code, only for the kill). The texts are
		// those of 5fd08069 in cells D1 and D3 of s3p2-linux2 and round
		// task073-03 (Linux VM), and W2-268 of round task073-01 (Windows VM).
		{name: "exit 128, no text", exit: 128, calls: 2, version: true,
			text: "git could not check this repository on the remote machine: at %[1]q: exit status 128"},
		{name: "exit 128, not a git repository", exit: 128, calls: exit128Runs(), secondOK: true,
			stderr: "fatal: not a git repository (or any of the parent directories): .git\n"},
		{name: "exit 128, another owner", exit: 128, calls: exit128Runs(), secondOK: true,
			stderr: "fatal: detected dubious ownership in repository at '/x/repo'\n"},
		{name: "exit 128, upper case", exit: 128, calls: exit128Runs(), secondOK: true, stderr: "FATAL: NOT A GIT REPOSITORY (x)\n"},
		{name: "exit 128, bare repository", exit: 128, calls: exit128Runs(), secondOK: true, stderr: "fatal: cannot use bare repository 'x'\n"},
		{name: "exit 128, not a git repository after a first line", exit: 128, calls: 2, version: true,
			stderr: "warning: w\nfatal: not a git repository\n",
			text:   "git could not check this repository on the remote machine: at %[1]q: warning: w fatal: not a git repository"},
		{name: "W2-268: exit 128, error reading", exit: 128, calls: 2, version: true,
			stderr: "fatal: error reading '/x/repo/.git'\n",
			text:   "git could not check this repository on the remote machine: at %[1]q: fatal: error reading '/x/repo/.git'"},
		{name: "another path", stdout: elsewhere + "\n", calls: 2,
			text: "git and Claude disagree about which repository holds this folder: from %[1]q git finds " +
				strings.ReplaceAll(strconv.Quote(elsewhere), "%", "%%") + ", Claude finds %[2]q; try again"},
		{name: "no output", calls: 2,
			text: "git and Claude disagree about which repository holds this folder: from %[1]q git finds %[1]q, Claude finds %[2]q; try again"},
	}, windowsRows...) {
		for i, method := range []string{"git.info", "git.status", "git.list_branches", "git.worktree_create", "git.worktree_remove"} {
			t.Run(a.name+"/"+method, func(t *testing.T) {
				f := newSeededShadowFixture(t)
				answerProbe(t, a.exit, a.stdout, a.stderr)
				if a.secondOK {
					answerSecondProbe(t, 0, "")
				}
				m := f.fiveRequests(nil)[i]
				raw, calls := f.call(t, m.method, m.params)
				var want string
				if a.text == "" {
					want = `{"jsonrpc":"2.0","id":1,` + f.noRepositoryAnswers()[i] + `}`
					if method == "git.info" {
						want = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}}`
					}
				} else {
					want = probeErrorShapes(t, f, fmt.Sprintf(a.text, f.repo, f.gitDir))[i]
				}
				if raw != want {
					t.Errorf("reply = %s\n  want %s", raw, want)
				}
				wantCalls := a.calls
				if method == "git.worktree_remove" {
					wantCalls *= 2
				}
				var others []string
				versions := 0
				for i, c := range calls {
					switch {
					case c.isProbe() || c.excludesRead():
					case a.version && c.plainVersion(t) && i > 0 && calls[i-1].isProbe():
						versions++
					default:
						others = append(others, strings.Join(c.argv, " "))
					}
				}
				if got := len(probeCalls(calls)); got != wantCalls || len(others) != 0 {
					t.Errorf("%d calls about the repository, want %d. Other calls: %q, want none", got, wantCalls, others)
				}
				if want := wantCalls / a.calls; a.version && versions != want {
					t.Errorf("%d plain version calls, want %d", versions, want)
				}
				if names := daemonFolders(t, f.run); len(names) == 1 {
					if got := entryNames(t, daemonFolder(t, f.run)); slices.Contains(got, shadowRepoName(f.gitDir)) {
						t.Errorf("the folder of the daemon holds %v, want no folder of the repository", got)
					}
				}
				if _, err := os.Stat(f.wt); err != nil {
					t.Errorf("the worktree of the fixture: %v", err)
				}
				if _, err := os.Stat(filepath.Join(f.repo, ".claude", "worktrees", "w1")); !os.IsNotExist(err) {
					t.Errorf("a worktree was made: %v", err)
				}
			})
		}
	}
}

// TestPrintedProbePath pins the path of git in the "disagree" text, with the
// separator of Windows on every system. The rows are table P2 of
// val-s3p2b2-7-windows (Windows VM): an answer with forward slashes and one
// with backslashes are each shown as printed. An answer that is not clean is
// not measured: it is shown cleaned. Without the compare the first row shows
// backslashes.
func TestPrintedProbePath(t *testing.T) {
	for _, row := range []struct{ out, found, want string }{
		{`C:/o/w/alt/.git`, `C:\o\w\alt\.git`, `C:/o/w/alt/.git`},
		{`C:\o\w\alt\.git`, `C:\o\w\alt\.git`, `C:\o\w\alt\.git`},
		{`C:/o/w/x/../alt/.git`, `C:\o\w\alt\.git`, `C:\o\w\alt\.git`},
		{`.git`, `C:\o\w\repo\.git`, `C:\o\w\repo\.git`},
	} {
		if got := printedProbePath(row.out, row.found, `\`); got != row.want {
			t.Errorf("printedProbePath(%q, %q) = %q, want %q", row.out, row.found, got, row.want)
		}
	}
}

// TestProbeGoodAnswerWithTheFullPath: an answer with the full path of the git
// directory is good, and it is kept for the next methods (cell 39 of
// s3p2b-edges-linux2, last wrapper row: 1, 0, 0, 0, 0 calls). After a bad
// answer every request asks again, and a good answer then gives a normal reply
// at once (cells 37 and 39). The test takes the first three methods: the loop
// checks "isRepo":true, which the results of git.worktree_create and
// git.worktree_remove do not have, and those two would change the fixture that
// the later rows read. The clock of the kept answer stands still, so a
// slow host cannot run past the kept time between the requests.
func TestProbeGoodAnswerWithTheFullPath(t *testing.T) {
	f := newSeededShadowFixture(t)
	setProbeClock(t)
	answerProbe(t, 128, "", "fatal: not a git repository (or any of the parent directories): .git\n")
	answerSecondProbe(t, 0, "")
	for range 2 {
		raw, calls := f.info(t, f.repo, nil)
		if !strings.Contains(raw, `"isRepo":false`) || len(probeCalls(calls)) != exit128Runs() {
			t.Fatalf("git.info with a bad answer = %s with %d calls about the repository, want %d each time", raw, len(probeCalls(calls)), exit128Runs())
		}
	}
	// Git prints the path with forward slashes on Windows.
	answerProbe(t, 0, filepath.ToSlash(f.gitDir)+"\n", "")
	want := []int{1, 0, 0}
	for i, m := range f.fiveRequests(nil)[:3] {
		raw, calls := f.call(t, m.method, m.params)
		if !strings.Contains(raw, `"isRepo":true`) {
			t.Errorf("%s = %s", m.method, raw)
		}
		if got := len(probeCalls(calls)); got != want[i] {
			t.Errorf("%s made %d calls about the repository, want %d", m.method, got, want[i])
		}
	}
}

// TestProbeKeep pins both sides of the kept time: 5 s from the END of the
// call, with no new time from a request inside it. It also pins that the
// answer is kept per work tree root (cell 34 of s3p2b-edges-linux2, Linux VM,
// and s3p2-linux cell B3b with s3p2-linux2 cell D7b).
func TestProbeKeep(t *testing.T) {
	f := newSeededShadowFixture(t)
	now := setProbeClock(t)
	start := *now
	count := func(path string) int {
		t.Helper()
		raw, calls := f.info(t, path, nil)
		if !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("git.info = %s", raw)
		}
		return len(probeCalls(calls))
	}
	if got := count(f.repo); got != 1 {
		t.Fatalf("the first request made %d calls, want 1", got)
	}
	sub := filepath.Join(f.repo, ".claude")
	for _, step := range []struct {
		after time.Duration
		path  string
		want  int
	}{
		{0, f.repo, 0},
		{time.Second, sub, 0},
		{2 * time.Second, f.wt, 1},
		{probeKeep - time.Millisecond, f.repo, 0},
		{probeKeep, f.repo, 1},
		{probeKeep + time.Second, f.repo, 0},
		{probeKeep + 1999*time.Millisecond, f.wt, 0},
		{probeKeep + 2*time.Second, f.wt, 1},
		{2*probeKeep - time.Millisecond, f.repo, 0},
		{2 * probeKeep, f.repo, 1},
	} {
		*now = start.Add(step.after)
		if got := count(step.path); got != step.want {
			t.Errorf("%v after the first call, on %s: %d calls, want %d", step.after, step.path, got, step.want)
		}
	}
}

// TestProbeAsksAgainAfterAChange: inside the kept time a new modification time
// or a new identity of the root of the work tree or of its `.git` makes the
// daemon ask again at once, and no other change does. The rows are those of
// cell 35 of s3p2b-edges-linux2 (Linux VM), and cell 25 of s3p2b-edges2-macos
// and s3p2b-edges2-windows for the first four and the config and HEAD rows. The
// request after a new call makes none.
func TestProbeAsksAgainAfterAChange(t *testing.T) {
	back := time.Now().Add(-100 * time.Second)
	touch := func(path string) {
		at := time.Now().Add(3 * time.Second)
		if err := os.Chtimes(path, at, at); err != nil {
			panic(err)
		}
	}
	for _, row := range []struct {
		name   string
		change func(f shadowFixture)
		want   int
	}{
		{"touch the root", func(f shadowFixture) { touch(f.repo) }, 1},
		{"a new folder in the root", func(f shadowFixture) { mustMkdir(filepath.Join(f.repo, "x")) }, 1},
		{"touch .git", func(f shadowFixture) { touch(f.gitDir) }, 1},
		{"a new file in .git", func(f shadowFixture) { mustWriteFile(filepath.Join(f.gitDir, "newfile"), "x") }, 1},
		{"the time of .git set back", func(f shadowFixture) { mustChtimes(f.gitDir, back) }, 1},
		{"the time of the root set back", func(f shadowFixture) { mustChtimes(f.repo, back) }, 1},
		{".git replaced by a copy with the old times", func(f shadowFixture) { replaceByCopy(f.repo, f.gitDir) }, 1},
		{"a new text of the config", func(f shadowFixture) {
			path := filepath.Join(f.gitDir, "config")
			b, _ := os.ReadFile(path)
			mustWriteFile(path, string(b)+"[core]\n\teol = lf\n")
		}, 0},
		{"touch the config", func(f shadowFixture) { touch(filepath.Join(f.gitDir, "config")) }, 0},
		{"HEAD rewritten in place", func(f shadowFixture) { mustWriteFile(filepath.Join(f.gitDir, "HEAD"), "ref: refs/heads/wr\n") }, 0},
		{"touch HEAD", func(f shadowFixture) { touch(filepath.Join(f.gitDir, "HEAD")) }, 0},
		{"touch .git/objects", func(f shadowFixture) { touch(filepath.Join(f.gitDir, "objects")) }, 0},
		{"touch .claude", func(f shadowFixture) { touch(filepath.Join(f.repo, ".claude")) }, 0},
		{"touch the parent folder", func(f shadowFixture) { touch(filepath.Dir(f.repo)) }, 0},
		{"chmod 700 .git", func(f shadowFixture) {
			if err := os.Chmod(f.gitDir, 0o700); err != nil {
				panic(err)
			}
		}, 0},
	} {
		t.Run(row.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.HasPrefix(row.name, "chmod") {
				t.Skip("no such mode on Windows")
			}
			f := newSeededShadowFixture(t)
			if runtime.GOOS != "windows" {
				// The control row of chmod: the mode differs before the change.
				if err := os.Chmod(f.gitDir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if _, calls := f.info(t, f.repo, nil); len(probeCalls(calls)) != 1 {
				t.Fatalf("the first request made %d calls, want 1", len(probeCalls(calls)))
			}
			row.change(f)
			raw, calls := f.info(t, f.repo, nil)
			if !strings.Contains(raw, `"isRepo":true`) {
				t.Fatalf("git.info after the change = %s", raw)
			}
			if got := len(probeCalls(calls)); got != row.want {
				t.Errorf("after the change: %d calls about the repository, want %d", got, row.want)
			}
			if _, calls := f.info(t, f.repo, nil); len(probeCalls(calls)) != 0 {
				t.Errorf("the request after that made %d calls, want 0", len(probeCalls(calls)))
			}
		})
	}
}

func mustMkdir(path string) {
	if err := os.Mkdir(path, 0o755); err != nil {
		panic(err)
	}
}

func mustWriteFile(path, text string) {
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		panic(err)
	}
}

func mustChtimes(path string, at time.Time) {
	if err := os.Chtimes(path, at, at); err != nil {
		panic(err)
	}
}

// replaceByCopy puts a copy of the folder gitDir in its place and gives the
// copy and root the modification times that they had before.
func replaceByCopy(root, gitDir string) {
	rootInfo, err := os.Stat(root)
	if err != nil {
		panic(err)
	}
	gitInfo, err := os.Stat(gitDir)
	if err != nil {
		panic(err)
	}
	old := gitDir + ".old"
	if err := os.Rename(gitDir, old); err != nil {
		panic(err)
	}
	if err := os.CopyFS(gitDir, os.DirFS(old)); err != nil {
		panic(err)
	}
	if err := os.RemoveAll(old); err != nil {
		panic(err)
	}
	mustChtimes(gitDir, gitInfo.ModTime())
	mustChtimes(root, rootInfo.ModTime())
}

// TestRefusedConfigInTheStateShadow: with a refused config the daemon in the
// state shadow makes no call of its own to git about the owner, and it reads
// the config again in each request. On 5fd08069 request 1 has the call about
// the repository and the read, and each later request the read alone
// (val-s3p2b2-3-linux section 1.4 item 1, Linux VM. val-s3p2b2-3-macos row D3.
// val-s3p2b2-3-windows row D7). No folder of the repository is made
// (s3p2-linux section 4).
func TestRefusedConfigInTheStateShadow(t *testing.T) {
	f := newSeededShadowFixture(t)
	setRepoConfig(t, f.gitDir, "[core]\n\trepositoryformatversion = 2\n"+baseRepoConfig)
	for request := 1; request <= 3; request++ {
		raw, calls := f.info(t, f.repo, nil)
		if !strings.Contains(raw, "the repository uses format version") {
			t.Fatalf("request %d = %s", request, raw)
		}
		var got []string
		for _, c := range calls {
			switch {
			case c.excludesRead():
			case c.isProbe():
				got = append(got, "the call about the repository")
			case c.configRead():
				got = append(got, "the config read")
			default:
				got = append(got, strings.Join(c.argv, " "))
			}
		}
		want := []string{"the config read"}
		if request == 1 {
			want = []string{"the call about the repository", "the config read"}
		}
		if !slices.Equal(got, want) {
			t.Errorf("request %d made %q, want %q", request, got, want)
		}
	}
	if got := entryNames(t, daemonFolder(t, f.run)); slices.Contains(got, shadowRepoName(f.gitDir)) {
		t.Errorf("the folder of the daemon holds %v, want no folder of the repository", got)
	}
}

// TestNoProbeOutsideTheStateShadow: a request with the member and a daemon in
// the state fallback-git make no call about the repository (s3-linux cell B1
// and cell 29 of s3p2b-edges-linux2, Linux VM).
func TestNoProbeOutsideTheStateShadow(t *testing.T) {
	f := newSeededShadowFixture(t)
	// A request without the member makes the folder of the daemon and asks. A
	// request with the member after the kept time makes no call.
	now := setProbeClock(t)
	if _, calls := f.info(t, f.repo, nil); len(probeCalls(calls)) != 1 {
		t.Fatalf("without the member: %d calls about the repository, want 1", len(probeCalls(calls)))
	}
	*now = now.Add(2 * probeKeep)
	if _, calls := f.info(t, f.repo, map[string]any{"shadowDirOff": true}); len(probeCalls(calls)) != 0 {
		t.Errorf("with the member: %d calls about the repository", len(probeCalls(calls)))
	}
	h := f.s.gitShadow
	h.mu.Lock()
	h.state = controlFallback
	h.mu.Unlock()
	raw, calls := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"settingsControl":"fallback-git"`) || len(probeCalls(calls)) != 0 {
		t.Errorf("in the state fallback-git: %s with %d calls about the repository", raw, len(probeCalls(calls)))
	}
}

// TestProbeKeepCountsFromTheEnd: the kept time counts from the END of the call.
// On 5fd08069 a request 4.50 s after the end of a call that its wrapper held
// for 2 s made no call. That request came 6.50 s after the start of the call
// (cell 34 of s3p2b-edges-linux2, Linux VM). Here the clock of the test moves 2
// s forward when the stub has logged the call.
func TestProbeKeepCountsFromTheEnd(t *testing.T) {
	f := newSeededShadowFixture(t)
	start := time.Now()
	const held = 2 * time.Second
	fixed := time.Time{}
	old := probeClock
	t.Cleanup(func() { probeClock = old })
	probeClock = func() time.Time {
		if !fixed.IsZero() {
			return fixed
		}
		if len(probeCalls(readEnvCalls(t, f.log))) > 0 {
			return start.Add(held)
		}
		return start
	}
	if _, calls := f.info(t, f.repo, nil); len(probeCalls(calls)) != 1 {
		t.Fatalf("the first request made %d calls about the repository, want 1", len(probeCalls(calls)))
	}
	for _, step := range []struct {
		afterEnd time.Duration
		want     int
	}{{probeKeep - 500*time.Millisecond, 0}, {probeKeep + 500*time.Millisecond, 1}} {
		fixed = start.Add(held + step.afterEnd)
		if _, calls := f.info(t, f.repo, nil); len(probeCalls(calls)) != step.want {
			t.Errorf("%v after the end of the call: %d calls about the repository, want %d", step.afterEnd, len(probeCalls(calls)), step.want)
		}
	}
}

// holdProbeOutput makes the stub of the call about the repository start a
// child that holds its stdout and stderr for hold, and exit 0. With git true
// the real git answers first, else the call prints nothing. The wait for the
// pipes keeps its production value of 1 s, so a loaded runner has 1 s to read
// what the real git printed, and the child outlives the wait.
func holdProbeOutput(t *testing.T, git bool, hold time.Duration) {
	t.Helper()
	mode := "holdonly"
	if git {
		mode = "hold"
	}
	endHeldDescendant(t)
	slowGit(t, "rev-parse,--git-dir", mode, hold, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_COMMON_DIR="+string(os.PathSeparator)+shadowProbeName)
}

// TestProbeExitWithHeldPipes: the call about the repository exits 0 while a
// child holds its pipes past the wait for them. The output read so far is the
// answer, and no `git version` call follows. With the git directory there,
// git.status answers as with no child (cells P1 and P2 of round task100-01,
// Linux VM, 34 calls on 5fd08069 in both). With no output, the daemon asks a
// second time and answers the "disagree" text (cell P3, 13 calls).
func TestProbeExitWithHeldPipes(t *testing.T) {
	const hold = 5 * time.Second
	status := func(t *testing.T, f shadowFixture) (string, []envCall) {
		t.Helper()
		m := f.fiveRequests(nil)[1]
		return f.call(t, m.method, m.params)
	}
	noVersion := func(t *testing.T, calls []envCall) {
		t.Helper()
		for _, c := range calls {
			if c.plainVersion(t) {
				t.Errorf("a plain `git version` call ran: %q", c.argv)
			}
		}
	}
	// count is the number of calls without the `--attr-source` test, which
	// runs once per process (attrSourceArgs).
	count := func(calls []envCall) int {
		return len(slices.DeleteFunc(slices.Clone(calls), func(c envCall) bool {
			return len(c.argv) == 2 && c.argv[1] == "version"
		}))
	}
	// The control has its own fixture in its own subtest: a second stub on
	// PATH would take the first one for the real git.
	var wantRaw string
	var wantCalls []envCall
	t.Run("P0: no child", func(t *testing.T) {
		wantRaw, wantCalls = status(t, newSeededShadowFixture(t))
	})
	t.Run("P1: the git directory", func(t *testing.T) {
		if wantCalls == nil {
			t.Skip("needs the control P0")
		}
		f := newSeededShadowFixture(t)
		holdProbeOutput(t, true, hold)
		raw, calls := status(t, f)
		if raw != wantRaw {
			t.Errorf("git.status = %s\n  want %s", raw, wantRaw)
		}
		if got := len(probeCalls(calls)); got != 1 || count(calls) != count(wantCalls) {
			t.Errorf("%d calls about the repository of %d calls, want 1 of %d", got, count(calls), count(wantCalls))
		}
		noVersion(t, calls)
	})
	t.Run("P3: no output", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		holdProbeOutput(t, false, hold)
		raw, calls := status(t, f)
		text := fmt.Sprintf("git and Claude disagree about which repository holds this folder: from %q git finds %q, Claude finds %q; try again",
			f.repo, f.repo, f.gitDir)
		if want := probeErrorShapes(t, f, text)[1]; raw != want {
			t.Errorf("git.status = %s\n  want %s", raw, want)
		}
		if got := len(probeCalls(calls)); got != 2 {
			t.Errorf("%d calls about the repository, want 2", got)
		}
		noVersion(t, calls)
	})
}
