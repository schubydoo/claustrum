//go:build unix

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/sessiondirs"
)

// With only gits in session folders on PATH, git.status answers the refusal and
// logs the passing-over line of each. A git that comes after the first request
// gets its line at the next request, inside the time that keeps the failed test.
func TestGitStatusLogsEachSessionFolderGit(t *testing.T) {
	t.Cleanup(sessiondirs.UseForTest(""))
	f := newShadowFixture(t)
	logs := captureLogBuf(t)
	stub, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(t.TempDir(), "bin"), filepath.Join(t.TempDir(), "bin")
	_ = sessiondirs.Remember(first, second)
	if err := os.Mkdir(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stub, filepath.Join(first, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", first+string(os.PathListSeparator)+second)
	want := `"message":"git cannot run on this host; git not run: ` + sessiondirs.NoOtherGitError{}.Error() + `"`
	params := map[string]any{"path": f.repo, "baseRepo": f.repo}
	passed := func(dir string) bool {
		return strings.Contains(logs.String(), `[sessiondirs] passing over "`+filepath.Join(dir, "git")+`"`)
	}
	if raw, _ := f.call(t, "git.status", params); !strings.Contains(raw, want) {
		t.Errorf("first git.status = %s\nwant %s", raw, want)
	}
	if !passed(first) || passed(second) {
		t.Errorf("log after the first request:\n%s", logs.String())
	}
	if err := os.Mkdir(second, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stub, filepath.Join(second, "git")); err != nil {
		t.Fatal(err)
	}
	if raw, _ := f.call(t, "git.status", params); !strings.Contains(raw, want) {
		t.Errorf("second git.status = %s\nwant %s", raw, want)
	}
	if !passed(second) {
		t.Errorf("no line for the second git:\n%s", logs.String())
	}
}
