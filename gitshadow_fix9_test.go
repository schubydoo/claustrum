package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestRelativeRunFolder: a daemon whose socket path is relative keeps the
// absolute form of its run folder, and every git method works. The test calls
// name absolute paths. On 5fd08069 the five methods answer as with the
// absolute path, and its test calls have `init -q --template=
// <cwd>/git-shadow/<S>/.check-<n>/real` (cell R1 of val-s3p2b2-7-linux2, Linux
// VM, with `-socket s.sock` and `-socket rel/rpc.sock`).
func TestRelativeRunFolder(t *testing.T) {
	for _, sock := range []string{"s.sock", filepath.Join("rel", "rpc.sock")} {
		t.Run(sock, func(t *testing.T) {
			f := newShadowFixture(t)
			wd := resolveTestRoot(t, t.TempDir())
			t.Chdir(wd)
			f.s.gitShadow = newShadowHost(execChildRunDir(sock))
			t.Cleanup(f.s.gitShadow.close)
			run := filepath.Join(wd, filepath.Dir(sock))
			raw, calls := f.info(t, f.repo, nil)
			if !strings.Contains(raw, `"isRepo":true`) || !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			dir := daemonFolder(t, run)
			named := 0
			for _, c := range calls {
				for _, word := range append(append([]string{c.cwd}, c.argv...), c.env...) {
					path := word
					if _, value, ok := strings.Cut(word, "="); ok {
						path = value
					}
					if strings.Contains(path, shadowRootName) && !filepath.IsAbs(path) {
						t.Errorf("a call names the relative path %q", path)
					}
					if strings.Contains(word, dir) {
						named++
					}
				}
			}
			if named == 0 {
				t.Errorf("no call names the folder of the daemon %s", dir)
			}
		})
	}
}

// TestInfoOnAPathThatDoesNotExist: a git.info on a path that does not exist
// runs no start test and no git call beyond the excludes read. As the first
// git request of a daemon its reply has no settingsControl member. The next
// request on a repository runs the start test. A later request on the path
// makes no call. A path that is a file still gets its `git version` call. On
// 5fd08069 the calls are 1, then 22, then 0 (cell F1 of val-s3p2b2-6-windows,
// Windows VM, 2 of 2 runs). Linux and macOS are not measured.
func TestInfoOnAPathThatDoesNotExist(t *testing.T) {
	f := newShadowFixture(t)
	gone := filepath.Join(filepath.Dir(f.repo), "no-such-folder")
	raw, calls := f.info(t, gone, nil)
	if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`; raw != want {
		t.Errorf("the first git.info = %s\nwant %s", raw, want)
	}
	if len(calls) != 1 || !calls[0].excludesRead() {
		t.Errorf("the first git.info made %d calls, want the excludes read alone", len(calls))
	}
	if got := daemonFolders(t, f.run); len(got) != 0 {
		t.Errorf("git-shadow holds %v after the first request, want no folder", got)
	}

	raw, calls = f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"settingsControl":"shadow"`) || len(testCalls(calls)) < 9 {
		t.Fatalf("git.info on the repository = %s with %d test calls, want the start test", raw, len(testCalls(calls)))
	}

	raw, calls = f.info(t, gone, nil)
	if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}}`; raw != want {
		t.Errorf("a later git.info = %s\nwant %s", raw, want)
	}
	if len(calls) != 0 {
		t.Errorf("a later git.info made %d calls, want none: %q", len(calls), calls[0].argv)
	}

	file := filepath.Join(filepath.Dir(f.repo), "a-file")
	mustWrite(t, file, "x")
	raw, calls = f.info(t, file, nil)
	versions := 0
	for _, c := range calls {
		if slices.Contains(c.argv, "version") {
			versions++
		}
	}
	// The reply for a file is that of each system: the hooks refusal on a
	// Windows VM, and the "no repository" answer here on Linux.
	if versions != 1 {
		t.Errorf("git.info on a file = %s with %d version calls, want 1", raw, versions)
	}
}
