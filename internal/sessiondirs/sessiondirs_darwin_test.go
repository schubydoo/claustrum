//go:build darwin

package sessiondirs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// On macOS a PATH entry that names a session folder in other letter case is passed
// over, by the device and the inode of the folder.
func TestPickGitMatchesOtherLetterCase(t *testing.T) {
	t.Cleanup(UseForTest(""))
	captureLog(t)
	s := filepath.Join(t.TempDir(), "Proj")
	bin := binWithGit(t, s)
	other := binWithGit(t, t.TempDir())
	Remember(s)
	lower := filepath.Join(filepath.Dir(s), strings.ToLower(filepath.Base(s)), "bin")
	a, errA := os.Stat(bin)
	b, errB := os.Stat(lower)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Skip("the volume of the test folder tells letter case apart")
	}
	t.Setenv("PATH", lower+":"+other)
	cmd := exec.Command("git")
	PickGit(cmd)
	if want := filepath.Join(other, "git"); cmd.Err != nil || cmd.Path != want {
		t.Errorf("git is %q (%v), want %q", cmd.Path, cmd.Err, want)
	}
}
