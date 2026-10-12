package main

import (
	"path/filepath"
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
