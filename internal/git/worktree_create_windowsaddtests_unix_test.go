//go:build unix

package git

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The tests of the Windows steps of git.worktree_create right after the add and
// after the checkout (windowsAddTests), run on Linux and macOS with the steps
// of Windows switched on. A symbolic link takes the place of the junction of
// the Windows rows. The frames are those of 4c111e86 (measured on Windows).

// setWindowsAddTests switches the Windows steps on for one test.
func setWindowsAddTests(t *testing.T) {
	t.Helper()
	old := windowsAddTests
	t.Cleanup(func() { windowsAddTests = old })
	windowsAddTests = true
}

// TestWorktreeCreateOffAddTestsOfWindows pins the rows of a create with
// "shadowDirOff":true where the stub changes the disk right after the add. Each
// create is refused with unsafe_path and runs no checkout. The leaf, the
// registration w1 and branch w1 stay.
func TestWorktreeCreateOffAddTestsOfWindows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps func(f wtFixture) [][]string
		text  func(f wtFixture) string
		leaf  []string
		reg   func(f wtFixture) string
	}{
		{"link at .git", func(f wtFixture) [][]string {
			g := filepath.Join(f.leaf(), ".git")
			return [][]string{{"rename", g, g + ".real"}, {"symlink", ".git.real", g}}
		}, func(f wtFixture) string {
			return "refusing to create worktree: `" + filepath.Join(f.leaf(), ".git") + "` is not a regular file"
		}, []string{".git", ".git.real"}, func(f wtFixture) string { return f.regDir }},
		{"folder at .git", func(f wtFixture) [][]string {
			g := filepath.Join(f.leaf(), ".git")
			return [][]string{{"rename", g, g + ".real"}, {"mkdir", g}}
		}, func(f wtFixture) string {
			return "refusing to create worktree: `" + filepath.Join(f.leaf(), ".git") + "` is not a regular file"
		}, []string{".git", ".git.real"}, func(f wtFixture) string { return f.regDir }},
		{"worktrees moved", func(f wtFixture) [][]string {
			return [][]string{{"rename", f.regDir, f.regDir + ".gone"}}
		}, func(f wtFixture) string {
			return "refusing to create worktree: `" + f.leaf() + "` does not hold the .git file made for it"
		}, []string{".git"}, func(f wtFixture) string { return f.regDir + ".gone" }},
		{"leaf copied", func(f wtFixture) [][]string {
			l := f.leaf()
			return [][]string{{"rename", l, l + ".old"}, {"mkdir", l}, {"snap", filepath.Join(l+".old", ".git"), filepath.Join(l, ".git")}}
		}, func(f wtFixture) string {
			return "refusing to create worktree: `" + f.leaf() + "` is no longer the directory that was created for the worktree"
		}, []string{".git"}, func(f wtFixture) string { return f.regDir }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setWindowsAddTests(t)
			f, h := postAddFixture(t)
			afterAddSteps(t, tc.steps(f)...)
			raw, _ := f.createWith(t, h, "w1", 0, map[string]any{"shadowDirOff": true})
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, tc.text(f)) +
				`,"errorCode":"unsafe_path"}}`
			if raw != want {
				t.Fatalf("reply = %s\nwant %s", raw, want)
			}
			if got := f.leafEntries(t); !slices.Equal(got, tc.leaf) {
				t.Errorf("leaf entries = %q, want %q", got, tc.leaf)
			}
			if _, err := os.Stat(filepath.Join(tc.reg(f), "w1", "HEAD")); err != nil {
				t.Errorf("the registration w1 lost its HEAD: %v", err)
			}
			if !f.hasRef(t, "w1") {
				t.Error("refs/heads/w1 is gone, want it kept")
			}
		})
	}
}

// TestWorktreeCreateGitFileGoneKeepsLeafOnWindows pins the row of 4c111e86 on
// Windows: the stub runs the real read-tree and then removes the .git file of
// the leaf. The create answers the frame of a gone .git file, and nothing is
// removed: the leaf keeps the files of the checkout, and the registration w1 and
// branch w1 stay.
func TestWorktreeCreateGitFileGoneKeepsLeafOnWindows(t *testing.T) {
	for name, extra := range map[string]map[string]any{"default": nil, "off": {"shadowDirOff": true}} {
		t.Run(name, func(t *testing.T) {
			setWindowsAddTests(t)
			f, h := postAddFixture(t)
			stubSteps(t, []string{"rm", filepath.Join(f.leaf(), ".git")})
			slowGit(t, "read-tree", "post", 0, "", "")

			raw, _ := f.createWith(t, h, "w1", 0, extra)
			f.wantGitFileGoneFrame(t, raw)
			if _, err := os.Stat(filepath.Join(f.leaf(), "t.txt")); err != nil {
				t.Errorf("the leaf lost the checkout: %v", err)
			}
			if got := f.regs(t); !slices.Equal(got, []string{"w1"}) {
				t.Errorf("registrations = %q, want w1 kept", got)
			}
			if !f.hasRef(t, "w1") {
				t.Error("refs/heads/w1 is gone, want it kept")
			}
		})
	}
}
