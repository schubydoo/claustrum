package main

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestShadowRelativeRunFolder: a daemon whose socket path is relative keeps the
// absolute form of its run folder, and every git method works. The test calls
// name absolute paths. On 5fd08069 the five methods answer as with the
// absolute path, and its test calls have `init -q --template=
// <cwd>/git-shadow/<S>/.check-<n>/real` (cell R1 of val-s3p2b2-7-linux2, Linux
// VM, with `-socket s.sock` and `-socket rel/rpc.sock`).
func TestShadowRelativeRunFolder(t *testing.T) {
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

// TestShadowInfoOnAPathThatDoesNotExist: a git.info on a path that does not exist
// runs no start test and no git call beyond the excludes read. As the first
// git request of a daemon its reply has no settingsControl member. The next
// request on a repository runs the start test. A later request on the path
// makes no call. A path that is a file still gets its `git version` call. On
// 5fd08069 the calls are 1, then 22, then 0 (cell F1 of val-s3p2b2-6-windows,
// Windows VM, 2 of 2 runs). A Linux VM and a macOS VM have 1, 20 and 0 calls
// (val-s3p2b2-9-linux2 and cell q1-missing of val-s3p2b2-9-macos).
func TestShadowInfoOnAPathThatDoesNotExist(t *testing.T) {
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

// TestShadowInfoOnAFileInARepository: a git.info on a file inside a repository
// is the first git request of a daemon. The request makes the call about the
// repository and the read of its config, and the daemon then has `.probe` and
// the copy. The next git.info on the repository makes neither call. On
// 5fd08069 the two requests have 14 and 8 calls, and the two calls are calls
// 12 and 13 of the first (row Q1 of val-s3p2b2-8-linux2, Linux VM). The reply
// of the first request is that of each system. macOS and Windows are not
// measured.
//
// Without the gate for the folder of the file, the first request makes neither
// call, and the second makes both.
func TestShadowInfoOnAFileInARepository(t *testing.T) {
	f := newShadowFixture(t)
	file := filepath.Join(f.repo, "a-file.txt")
	mustWrite(t, file, "x\n")
	raw, calls := f.info(t, file, nil)
	if runtime.GOOS != "windows" {
		if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}}`; raw != want {
			t.Errorf("git.info on the file = %s\nwant %s", raw, want)
		}
	}
	probes, reads := probeCalls(calls), configReads(calls)
	// One read is call 7 of the start test, and one is the read of the config.
	if len(probes) != 1 || len(reads) != 2 {
		t.Fatalf("git.info on the file made %d calls about the repository and %d config reads, want 1 and 2", len(probes), len(reads))
	}
	if cwd := probes[0].cwd; cwd != f.repo {
		t.Errorf("the call about the repository ran in %s, want %s", cwd, f.repo)
	}
	names := entryNames(t, daemonFolder(t, f.run))
	for _, want := range []string{shadowProbeName, shadowRepoName(f.gitDir)} {
		if !slices.Contains(names, want) {
			t.Errorf("the folder of the daemon holds %v, want %s too", names, want)
		}
	}
	raw, calls = f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"isRepo":true`) || len(probeCalls(calls)) != 0 || len(configReads(calls)) != 0 {
		t.Errorf("the next git.info = %s with %d calls about the repository and %d config reads, want a normal reply and none",
			raw, len(probeCalls(calls)), len(configReads(calls)))
	}
}

// TestShadowInfoOnAFileOutsideARepository: a git.info on a regular file
// outside any repository is the first git request of a daemon. It runs no
// start test: the request makes the excludes read and one `version` call, the
// reply has no settingsControl member, and no folder `git-shadow` is made. The
// next git.info on a repository runs the start test. On 5fd08069 the two
// requests have 2 and 20 calls (cell q1-file of val-s3p2b2-8-macos, macOS VM,
// 2 of 2 runs). The reply of the first request is that of each system. Linux
// and Windows are not measured.
//
// With the gate of a path that names no folder, the first request runs the
// start test and has the member.
func TestShadowInfoOnAFileOutsideARepository(t *testing.T) {
	f := newShadowFixture(t)
	file := filepath.Join(filepath.Dir(f.repo), "plainfile.txt")
	mustWrite(t, file, "x\n")
	raw, calls := f.info(t, file, nil)
	if runtime.GOOS != "windows" {
		if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`; raw != want {
			t.Errorf("git.info on the file = %s\nwant %s", raw, want)
		}
	}
	if len(calls) != 2 || !calls[0].excludesRead() || !slices.Contains(calls[1].argv, "version") {
		t.Errorf("git.info on the file made %d calls, want the excludes read and one version call", len(calls))
	}
	if got := daemonFolders(t, f.run); len(got) != 0 {
		t.Errorf("git-shadow holds %v after the first request, want no folder", got)
	}
	raw, calls = f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"settingsControl":"shadow"`) || len(testCalls(calls)) < 9 {
		t.Errorf("git.info on the repository = %s with %d test calls, want the start test", raw, len(testCalls(calls)))
	}
}
