//go:build unix

package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestShadowInfoOnANonRegularFileOutsideARepository: the first git.info of a
// daemon on a FIFO or on /dev/null outside any repository runs no start test.
// Its reply has no settingsControl member. b3081b51 answers
// `{"isRepo":false,"repoSlug":"","defaultBranch":""}` for both paths (measured
// on Linux).
func TestShadowInfoOnANonRegularFileOutsideARepository(t *testing.T) {
	want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`
	for _, name := range []string{"fifo", "null device"} {
		t.Run(name, func(t *testing.T) {
			f := newShadowFixture(t)
			path := os.DevNull
			if name == "fifo" {
				path = filepath.Join(filepath.Dir(f.repo), "fifo")
				mustMkfifo(t, path)
				defer releaseFifoReader(path)
			}
			if raw, _ := f.info(t, path, nil); raw != want {
				t.Errorf("git.info on the %s = %s\nwant %s", name, raw, want)
			}
		})
	}
}

// TestShadowStatusOnANonRegularFileOutsideARepository: a first git.status of a
// daemon with path and baseRepo on a FIFO or on /dev/null outside any
// repository runs no start test. b3081b51 makes 2 calls there, the excludes
// read and `version`, and the git.info after it has no settingsControl member
// (measured on Linux).
func TestShadowStatusOnANonRegularFileOutsideARepository(t *testing.T) {
	wantStatus := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"clean":false}}`
	wantInfo := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`
	wantCalls := []string{"config --includes --path core.excludesFile", "version"}
	for _, name := range []string{"fifo", "null device"} {
		t.Run(name, func(t *testing.T) {
			f := newShadowFixture(t)
			path := os.DevNull
			if name == "fifo" {
				path = filepath.Join(filepath.Dir(f.repo), "fifo")
				mustMkfifo(t, path)
				defer releaseFifoReader(path)
			}
			raw, calls := f.call(t, "git.status", map[string]any{"path": path, "baseRepo": path})
			if raw != wantStatus {
				t.Errorf("git.status on the %s = %s\nwant %s", name, raw, wantStatus)
			}
			got := []string{}
			for _, c := range calls {
				got = append(got, strings.Join(c.argv, " "))
			}
			if !slices.Equal(got, wantCalls) {
				t.Errorf("git.status on the %s made the calls %q, want %q", name, got, wantCalls)
			}
			if names := daemonFolders(t, f.run); len(names) != 0 {
				t.Errorf("git.status on the %s left git-shadow with %v, want no folder", name, names)
			}
			if raw, _ := f.info(t, path, nil); raw != wantInfo {
				t.Errorf("git.info after git.status on the %s = %s\nwant %s", name, raw, wantInfo)
			}
		})
	}
}

// TestShadowStatusOnAFileInsideARepository: a git.status whose baseRepo is a
// FIFO or a regular file inside a repository keeps the gate, so the start test
// runs (claustrum's own, not measured).
func TestShadowStatusOnAFileInsideARepository(t *testing.T) {
	for _, name := range []string{"fifo", "regular file"} {
		t.Run(name, func(t *testing.T) {
			f := newShadowFixture(t)
			path := filepath.Join(f.repo, "inside")
			if name == "fifo" {
				mustMkfifo(t, path)
				defer releaseFifoReader(path)
			} else if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			_, calls := f.call(t, "git.status", map[string]any{"path": path, "baseRepo": path})
			if len(testCalls(calls)) == 0 {
				t.Errorf("git.status on a %s inside the repository ran no start test, calls %d", name, len(calls))
			}
		})
	}
}
