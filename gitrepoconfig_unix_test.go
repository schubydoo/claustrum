//go:build unix

package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The Linux and macOS tests of the config read: the file kinds, and the include
// values that are refused on Windows by another rule.

func notRegularDetail(path string) string {
	return strconv.Quote(path) + " is not a regular file"
}

// answerInTime runs one request and fails when it does not answer in 10 s. A
// read that waits in the open of a FIFO gives no answer.
func answerInTime(t *testing.T, s *server, method string, params map[string]any) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- dispatchRaw(t, s, rpcLine(t, method, params)) }()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not answer in 10 s", method)
		return ""
	}
}

// detailInTime runs the read of the config and fails when it does not end in
// 10 s.
func detailInTime(t *testing.T, gitDir string) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- configDetail(t, gitDir) }()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("the read of the config did not end in 10 s")
		return ""
	}
}

// TestRepoConfigNotRegular pins the kind test of the config (cells C4 and X10):
// a FIFO, a symbolic link to the null device and a directory are refused. A
// symbolic link to a regular file, a link to such a link and a dangling link
// are not. No request waits on the FIFO.
func TestRepoConfigNotRegular(t *testing.T) {
	f := newRefusalFixture(t)
	config := filepath.Join(f.gitDir, "config")
	realFile := filepath.Join(filepath.Dir(f.repo), "realconfig")
	if err := os.Rename(config, realFile); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	quoted, _ := json.Marshal(repoConfigPrefix + notRegularDetail(config))
	want := `"error":{"code":-32603,"message":` + string(quoted) + `}`
	refused := func(state string) {
		t.Helper()
		for _, m := range f.fiveRequests(nil)[:2] {
			if raw := answerInTime(t, s, m.method, m.params); !strings.Contains(raw, want) {
				t.Errorf("%s with a config that is %s = %s, want %s", m.method, state, raw, want)
			}
		}
	}

	mustMkfifo(t, config)
	// If a read waits in its open, this writer open lets it go at the end.
	defer releaseFifoReader(config)
	refused("a FIFO")
	mustRemove(t, config)

	mustSymlink(t, os.DevNull, config)
	refused("a link to the null device")
	mustRemove(t, config)

	if err := os.Mkdir(config, 0o755); err != nil {
		t.Fatal(err)
	}
	refused("a directory")
	mustRemove(t, config)

	second := filepath.Join(filepath.Dir(f.repo), "link")
	mustSymlink(t, realFile, second)
	for state, target := range map[string]string{"a link to a file": realFile, "a link to a link to a file": second,
		"a dangling link": filepath.Join(filepath.Dir(f.repo), "no-such-file")} {
		mustSymlink(t, target, config)
		if raw := answerInTime(t, s, "git.info", map[string]any{"path": f.repo}); !strings.Contains(raw, `"result":{"isRepo":true,`) {
			t.Errorf("git.info with a config that is %s = %s, want a repository", state, raw)
		}
		mustRemove(t, config)
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// TestRepoConfigOpenErrors pins the text of a config file that the daemon
// cannot open: the error of the open, with the path not in quotes. Cells X10
// ran the config, and cells V7 an included file (mode 000 on Linux and macOS
// VMs, a socket on a Linux VM). The socket rows are for Linux: the error text of
// an open of a socket is another one on macOS. A path of 400 bytes is not cut in
// this text (cells V12, Linux and macOS VMs).
func TestRepoConfigOpenErrors(t *testing.T) {
	t.Run("mode 000", func(t *testing.T) {
		skipIfRoot(t)
		_, gitDir := configRepo(t)
		config := filepath.Join(gitDir, "config")
		// The included file is in two folders of 190 bytes, so its path has over
		// 400 bytes.
		deep := filepath.Join(strings.Repeat("h", 190), strings.Repeat("j", 190))
		if err := os.MkdirAll(filepath.Join(gitDir, deep), 0o755); err != nil {
			t.Fatal(err)
		}
		inc := filepath.Join(gitDir, deep, "inc.cfg")
		setIncludeFile(t, gitDir, filepath.Join(deep, "inc.cfg"), "[x]\n\tinc = 1\n")
		setRepoConfig(t, gitDir, version0+"[include]\n\tpath = "+deep+"/inc.cfg\n")
		if err := os.Chmod(inc, 0); err != nil {
			t.Fatal(err)
		}
		if got, want := configDetail(t, gitDir), "open "+inc+": permission denied"; got != want {
			t.Errorf("included file of mode 000: detail = %q, want %q", got, want)
		}
		if err := os.Chmod(config, 0); err != nil {
			t.Fatal(err)
		}
		if got, want := configDetail(t, gitDir), "open "+config+": permission denied"; got != want {
			t.Errorf("config of mode 000: detail = %q, want %q", got, want)
		}
	})
	t.Run("socket", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("the error text of an open of a socket is for Linux")
		}
		// The socket path has a length limit, so the repository is in a short folder.
		requireGit(t)
		dir, err := os.MkdirTemp("", "cl")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		repo := filepath.Join(resolveTestRoot(t, dir), "r")
		if err := os.Mkdir(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "init", "-q", "-b", "main")
		config := filepath.Join(repo, ".git", "config")
		inc := filepath.Join(repo, ".git", "i.cfg")
		incLn, err := net.Listen("unix", inc)
		if err != nil {
			t.Fatalf("bind a socket at %s (%d bytes): %v", inc, len(inc), err)
		}
		defer func() { _ = incLn.Close() }()
		setRepoConfig(t, filepath.Join(repo, ".git"), version0+"[include]\n\tpath = i.cfg\n")
		if got, want := configDetail(t, filepath.Join(repo, ".git")), "open "+inc+": no such device or address"; got != want {
			t.Errorf("included file that is a socket: detail = %q, want %q", got, want)
		}
		mustRemove(t, config)
		ln, err := net.Listen("unix", config)
		if err != nil {
			t.Fatalf("bind a socket at %s (%d bytes): %v", config, len(config), err)
		}
		defer func() { _ = ln.Close() }()
		if got, want := configDetail(t, filepath.Join(repo, ".git")), "open "+config+": no such device or address"; got != want {
			t.Errorf("config that is a socket: detail = %q, want %q", got, want)
		}
	})
}

// TestRepoConfigIncludeNotRegular pins the kind test of an included file (cells
// E10 and X9): a FIFO and a directory are refused with the path of that file. A
// symbolic link to a FIFO is refused with the path of the link. The file of an
// `includeIf "gitdir:<repo>/"` section that matches gets the test too.
func TestRepoConfigIncludeNotRegular(t *testing.T) {
	repo, gitDir := configRepo(t)
	fifo, dir, link := filepath.Join(gitDir, "fifo.cfg"), filepath.Join(gitDir, "dir.cfg"), filepath.Join(filepath.Dir(repo), "lnkf.cfg")
	mustMkfifo(t, fifo)
	defer releaseFifoReader(fifo)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, fifo, link)
	for _, c := range []struct{ section, path string }{
		{"[include]\n\tpath = fifo.cfg\n", fifo},
		{"[include]\n\tpath = dir.cfg\n", dir},
		{"[include]\n\tpath = " + link + "\n", link},
		{"[includeIf \"gitdir:" + repo + "/\"]\n\tpath = fifo.cfg\n", fifo},
		// A condition that does not match (cell V8, Linux and macOS VMs).
		{"[includeIf \"gitdir:/nonexistent/zz/\"]\n\tpath = fifo.cfg\n", fifo},
	} {
		setRepoConfig(t, gitDir, version0+c.section)
		if got, want := detailInTime(t, gitDir), notRegularDetail(c.path); got != want {
			t.Errorf("%q: detail = %q, want %q", c.section, got, want)
		}
	}
}

// TestGitDirInfoFilesKinds pins the kind test of info/attributes and
// info/exclude (cells C5 and X10): a FIFO and a directory are refused. A
// symbolic link to a file, one to the null device, a dangling one and a file of
// mode 000 are not.
func TestGitDirInfoFilesKinds(t *testing.T) {
	f := newRefusalFixture(t)
	s := newTestServer(t)
	target := filepath.Join(filepath.Dir(f.repo), "target")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"attributes", "exclude"} {
		path := filepath.Join(f.gitDir, "info", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(path)
		quoted, _ := json.Marshal(repoFilePrefix + notRegularDetail(path))
		want := `"error":{"code":-32603,"message":` + string(quoted) + `}`

		mustMkfifo(t, path)
		// If a read waits in its open, this writer open lets it go at the end.
		defer releaseFifoReader(path)
		for _, m := range f.fiveRequests(nil)[:2] {
			if raw := answerInTime(t, s, m.method, m.params); !strings.Contains(raw, want) {
				t.Errorf("%s with a FIFO as info/%s = %s, want %s", m.method, name, raw, want)
			}
		}
		mustRemove(t, path)

		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if got, want := gitDirFilesDetail(f.gitDir, nil), notRegularDetail(path); got != want {
			t.Errorf("info/%s as a directory: detail = %q, want %q", name, got, want)
		}
		mustRemove(t, path)

		for _, to := range []string{target, os.DevNull, filepath.Join(filepath.Dir(f.repo), "no-such-file")} {
			mustSymlink(t, to, path)
			if got := gitDirFilesDetail(f.gitDir, nil); got != "" {
				t.Errorf("info/%s as a link to %s: detail = %q, want none", name, to, got)
			}
			mustRemove(t, path)
		}

		if os.Geteuid() != 0 {
			if err := os.WriteFile(path, []byte("x\n"), 0); err != nil {
				t.Fatal(err)
			}
			if got := gitDirFilesDetail(f.gitDir, nil); got != "" {
				t.Errorf("info/%s of mode 000: detail = %q, want none", name, got)
			}
			mustRemove(t, path)
		}
	}
}

// TestGitDirShallowKinds pins the kind test of `shallow` (cells C5 and X10): a
// FIFO, a directory and a symbolic link to the null device are refused as not
// regular. A file of mode 000 gets the error of the open. A symbolic link to a
// file and a dangling link are not refused.
func TestGitDirShallowKinds(t *testing.T) {
	f := newRefusalFixture(t)
	s := newTestServer(t)
	path := filepath.Join(f.gitDir, "shallow")
	target := filepath.Join(filepath.Dir(f.repo), "target")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(repoFilePrefix + notRegularDetail(path))
	want := `"error":{"code":-32603,"message":` + string(quoted) + `}`

	mustMkfifo(t, path)
	defer releaseFifoReader(path)
	for _, m := range f.fiveRequests(nil)[:2] {
		if raw := answerInTime(t, s, m.method, m.params); !strings.Contains(raw, want) {
			t.Errorf("%s with a FIFO as shallow = %s, want %s", m.method, raw, want)
		}
	}
	mustRemove(t, path)

	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := gitDirFilesDetail(f.gitDir, nil), notRegularDetail(path); got != want {
		t.Errorf("shallow as a directory: detail = %q, want %q", got, want)
	}
	mustRemove(t, path)

	mustSymlink(t, os.DevNull, path)
	if got, want := gitDirFilesDetail(f.gitDir, nil), notRegularDetail(path); got != want {
		t.Errorf("shallow as a link to the null device: detail = %q, want %q", got, want)
	}
	mustRemove(t, path)

	for _, to := range []string{target, filepath.Join(filepath.Dir(f.repo), "no-such-file")} {
		mustSymlink(t, to, path)
		if got := gitDirFilesDetail(f.gitDir, nil); got != "" {
			t.Errorf("shallow as a link to %s: detail = %q, want none", to, got)
		}
		mustRemove(t, path)
	}

	if os.Geteuid() != 0 {
		if err := os.WriteFile(path, nil, 0); err != nil {
			t.Fatal(err)
		}
		if got, want := gitDirFilesDetail(f.gitDir, nil), "open "+path+": permission denied"; got != want {
			t.Errorf("shallow of mode 000: detail = %q, want %q", got, want)
		}
	}
}

// TestRepoConfigIncludeNotExpanded pins the refusal of `~user` and `%(prefix)`
// (cells C3). The text names the file that holds the line.
func TestRepoConfigIncludeNotExpanded(t *testing.T) {
	_, gitDir := configRepo(t)
	unexpanded := func(file, value string) string {
		return strconv.Quote(filepath.Join(gitDir, file)) + " includes " + strconv.Quote(value) +
			", and Claude expands neither ~user nor %(prefix), nor ~ where no home directory is set; write that path out in full"
	}
	for _, value := range []string{"~root/x", "%(prefix)/x"} {
		setRepoConfig(t, gitDir, version0+"[include]\n\tpath = "+value+"\n")
		if got, want := configDetail(t, gitDir), unexpanded("config", value); got != want {
			t.Errorf("include %s: detail = %q, want %q", value, got, want)
		}
	}
	// The value in the text is cut to its first 100 characters: 100 are whole and
	// 101 are cut, also with characters of two bytes (cells V12, Linux and macOS
	// VMs).
	for _, fill := range []string{"v", "é"} {
		for _, n := range []int{100, 101, 400} {
			value := "~root/" + strings.Repeat(fill, n-len("~root/"))
			setRepoConfig(t, gitDir, version0+"[include]\n\tpath = "+value+"\n")
			if got, want := configDetail(t, gitDir), unexpanded("config", string([]rune(value)[:100])); got != want {
				t.Errorf("include value of %d characters of %s: detail = %q, want %q", n, fill, got, want)
			}
		}
	}
	// The line of an included file names that file.
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = inc.cfg\n")
	setIncludeFile(t, gitDir, "inc.cfg", "[include]\n\tpath = ~root/x\n")
	if got, want := configDetail(t, gitDir), unexpanded("inc.cfg", "~root/x"); got != want {
		t.Errorf("include line in an included file: detail = %q, want %q", got, want)
	}
	// No cell: no daemon starts with no home folder. claustrum answers the same text.
	t.Setenv("HOME", "")
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = ~/x\n")
	if got, want := configDetail(t, gitDir), unexpanded("config", "~/x"); got != want {
		t.Errorf("include ~/x with no home folder: detail = %q, want %q", got, want)
	}
}

// TestRepoConfigIncludePlaces pins that `~/x` with a home folder, an absolute
// include and a relative include that leaves the git directory are followed
// (cells C3 and X9).
func TestRepoConfigIncludePlaces(t *testing.T) {
	repo, gitDir := configRepo(t)
	home := filepath.Join(filepath.Dir(repo), "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	x := filepath.Join(home, "x")
	if err := os.WriteFile(x, []byte(markerInclude), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	// The path of a relative include is the folder of its file, a separator and
	// the value as written. A Windows VM measured that form (cells V12c and V12d).
	for value, path := range map[string]string{"~/x": x, x: x, "../../home/x": gitDir + "/../../home/x"} {
		setRepoConfig(t, gitDir, version0+"[include]\n\tpath = "+value+"\n")
		if got, want := configDetail(t, gitDir), markerDetail(path); got != want {
			t.Errorf("include %s: detail = %q, want %q", value, got, want)
		}
	}
	// The folder of an included file stays as written for the files that it
	// includes (cell N13-b, Linux and macOS VMs). The last file of that cell is a FIFO.
	if err := os.MkdirAll(filepath.Join(gitDir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(gitDir, "b", "inc.cfg"), "[include]\n\tpath = x.cfg\n", 0o644)
	writeFile(t, filepath.Join(gitDir, "b", "x.cfg"), markerInclude, 0o644)
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = a/../b/inc.cfg\n")
	if got, want := configDetail(t, gitDir), markerDetail(gitDir+"/a/../b/x.cfg"); got != want {
		t.Errorf("include of an included file below a/../b: detail = %q, want %q", got, want)
	}
	// `include.path = ~` names the home folder itself (cell N13, Linux VM).
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = ~\n")
	if got, want := configDetail(t, gitDir), notRegularDetail(home); got != want {
		t.Errorf("include ~: detail = %q, want %q", got, want)
	}
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = "+filepath.Join(home, "missing")+"\n")
	if got := configDetail(t, gitDir); got != "" {
		t.Errorf("absolute include of a missing file: detail = %q, want none", got)
	}
}

// TestRepoConfigRefusalWithWorktreeRoot pins git.worktree_remove with
// worktreeRoot in a repository that the daemon refuses. 5fd08069 answers the
// "cannot determine the repository's work tree" text with the refusal of the
// read, and removes nothing (cells N14, Linux and macOS VMs). The states of those cells are
// format version 2 and a config of 20 MiB. Each ran for a worktree that exists
// and for a folder that is missing. In a good repository the worktree is
// removed.
func TestRepoConfigRefusalWithWorktreeRoot(t *testing.T) {
	f := newWRFixture(t)
	config := filepath.Join(f.T, ".git", "config")
	good, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(f.R, "proj", "nope")
	for _, c := range []struct{ name, text, want string }{
		{"format version 2", versionConfig("repositoryformatversion = 2"), repoConfigPrefix + versionDetail("2")},
		{"a config over 8 MiB", version0 + strings.Repeat("#c\n", (repoConfigMaxBytes+3)/3),
			repoConfigPrefix + strconv.Quote(config) + " is larger than 8 MiB"},
	} {
		writeFile(t, config, c.text, 0o644)
		for _, target := range []string{f.e0, gone} {
			if got, want := f.remove(t, f.T, target, "e0"), refusalFrame(t, wtUnknown+c.want); got != want {
				t.Errorf("%s, remove of %s = %s, want %s", c.name, target, got, want)
			}
			f.kept(t, f.e0, "e0")
		}
	}
	writeFile(t, config, string(good), 0o644)
	if got, want := f.remove(t, f.T, f.e0, "e0"), `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
		t.Errorf("good repository, remove = %s, want %s", got, want)
	}
	f.removed(t, f.e0, "e0")
}
