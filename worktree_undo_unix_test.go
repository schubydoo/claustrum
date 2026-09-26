//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWorktreeCreateUndoCouldNotFinish pins the two undo texts of a rollback after
// a successful add. The steps and texts were measured against f6010b97 and 90fca6e6
// on a Windows VM, with a process holding a file or a directory in the leaf. Here a
// read-only directory makes the same steps fail on Linux and macOS: the git stub
// makes it during the call under test (gitStubAction "lock" and "lockparent").
//
//   - Step A: <leaf>/hsub is read-only and holds a file, so hsub cannot be
//     deleted. The undo deletes the entries in the order that the directory read
//     returns them, and stops at hsub. The entries from hsub on, the registration
//     and w1 all stay. The stub records that order (CLAUSTRUM_GITSTUB_SNAP).
//   - Step C: the leaf's parent is read-only, so the empty leaf cannot be removed.
//     The registration and w1 are gone.
//
// A read-only mode does not stop root, so the test skips as root. The OS error text
// is the text Go gives for EACCES. Both references gave the same text on Linux and
// macOS VMs.
func TestWorktreeCreateUndoCouldNotFinish(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)
	full := calibrate(t, realGit)
	lateMs := int((2*full + 500*time.Millisecond).Milliseconds())

	const stepA = "; and the undo could not finish for %s: the worktree directory, its registration, and the branch all remain; remove them by hand before retrying (RemoveAll hsub: permission denied)"
	const stepC = "; and the undo could not finish for %s: the worktree directory remains (re-populated while undoing?); remove it by hand before retrying (removeat w1: permission denied)"
	for _, tc := range []struct {
		name, match, mode, action string
		d                         time.Duration
		timeoutMs                 int
		frame, code, undo         string
	}{
		{"A checkout failure", "read-tree", "fail", "lock", 0, 0,
			"git worktree add failed (checkout): fatal: synthetic checkout failure", "worktree_add_failed", stepA},
		{"A add timeout", "worktree,add", "post", "lock", 1500 * time.Millisecond, 100,
			"git worktree add timed out after 100ms (deadline expired before the checkout started)", "timeout", stepA},
		// timeoutMs -1 stands for the late deadline, and "during the checkout" for
		// the killed checkout's frame.
		{"A killed checkout", "read-tree", "pre", "lock", 60 * time.Second, -1,
			"during the checkout", "timeout", stepA},
		// The checkout filled the leaf, so the leaf holds more entries before and
		// after hsub.
		{"A drain overrun", "read-tree", "hold", "lock", 60 * time.Second, -1,
			"after the checkout finished", "timeout", stepA},
		{"C checkout failure", "read-tree", "fail", "lockparent", 0, 0,
			"git worktree add failed (checkout): fatal: synthetic checkout failure", "worktree_add_failed", stepC},
		{"C add timeout", "worktree,add", "post", "lockparent", 1500 * time.Millisecond, 100,
			"git worktree add timed out after 100ms (deadline expired before the checkout started)", "timeout", stepC},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWTFixture(t, false)
			s := newTestServer(t)
			t.Cleanup(func() {
				// Give the temp-dir cleanup its write access back.
				_ = os.Chmod(filepath.Join(f.leaf(), "hsub"), 0o755)
				_ = os.Chmod(filepath.Dir(f.leaf()), 0o755)
			})
			t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
			f.stubAction(t, tc.action)
			snap := filepath.Join(t.TempDir(), "snap.txt")
			t.Setenv("CLAUSTRUM_GITSTUB_SNAP", snap)
			stderr := ""
			if tc.mode == "fail" {
				stderr = `fatal: synthetic checkout failure\n`
			}
			timeoutMs, frame := tc.timeoutMs, tc.frame
			if timeoutMs < 0 {
				timeoutMs = lateMs
				head := "git worktree add timed out after " + strconv.Itoa(lateMs) + "ms (deadline expired "
				if tc.mode == "hold" {
					// The checkout exits 0 and a descendant holds its pipes past the
					// drain cap, which ends after the deadline.
					oldCap := worktreeCreateDrainCap
					t.Cleanup(func() { worktreeCreateDrainCap = oldCap })
					worktreeCreateDrainCap = time.Duration(lateMs)*time.Millisecond + time.Second
					frame = head + tc.frame + ")"
				} else {
					frame = head + tc.frame + "): signal: killed"
				}
			}
			slowGit(t, tc.match, tc.mode, tc.d, stderr, "")
			raw, _ := f.create(t, s, "w1", "", timeoutMs)
			wantError(t, raw, frame+strings.ReplaceAll(tc.undo, "%s", f.leaf()), tc.code)
			if tc.undo == stepA {
				order := readSnap(t, snap)
				i := slices.Index(order, "hsub")
				if i < 0 {
					t.Fatalf("stub snapshot %q holds no hsub", order)
				}
				want := slices.Sorted(slices.Values(order[i:]))
				if got := f.leafEntries(t); !slices.Equal(got, want) {
					t.Errorf("leaf entries = %q, want %q: the undo deletes in read order %q and stops at hsub", got, want, order)
				}
				if got := f.regs(t); !slices.Equal(got, []string{"w1"}) {
					t.Errorf("registrations = %q, want w1 kept", got)
				}
				if !f.hasRef(t, "w1") {
					t.Errorf("refs/heads/w1 was deleted, want it kept after a step A failure")
				}
				return
			}
			if got := f.leafEntries(t); !slices.Equal(got, []string{}) {
				t.Errorf("leaf entries = %q, want an empty leaf", got)
			}
			if got := f.regs(t); !slices.Equal(got, []string{}) {
				t.Errorf("registrations = %q, want none", got)
			}
			if f.hasRef(t, "w1") {
				t.Errorf("refs/heads/w1 exists, want it deleted before step C")
			}
		})
	}
}

// readSnap returns the leaf's names in the order that the git stub read them
// (CLAUSTRUM_GITSTUB_SNAP).
func readSnap(t *testing.T, snap string) []string {
	t.Helper()
	b, err := os.ReadFile(snap)
	if err != nil {
		t.Fatalf("stub snapshot: %v", err)
	}
	return strings.Split(string(b), "\n")
}

// TestWorktreeUndoRawReadDirOrder pins the order of undo step A. Both references
// delete the leaf's top-level entries in the order that the directory read returns
// them, not sorted, and stop at the first entry that fails. The frame names that
// entry. Measured against f6010b97 and 90fca6e6 on Linux ext4 and macOS APFS VMs.
//
// The stub makes r0 to r7 in the leaf, in a mixed order, each read-only and
// holding a file. It records the read order. The undo must fail at the first rN in
// that order, keep that entry and every entry after it, and delete the entries
// before it. The frame tells read order from byte order only when the first rN is
// not r0. The test retries with a fresh fixture until one run does, and skips if
// none does (a file system that returns sorted names).
func TestWorktreeUndoRawReadDirOrder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)
	const stepA = "; and the undo could not finish for %s: the worktree directory, its registration, and the branch all remain; remove them by hand before retrying (RemoveAll %s: permission denied)"
	for attempt := range 10 {
		told := false
		t.Run("attempt "+strconv.Itoa(attempt), func(t *testing.T) {
			f := newWTFixture(t, false)
			s := newTestServer(t)
			t.Cleanup(func() {
				for i := range 8 {
					_ = os.Chmod(filepath.Join(f.leaf(), "r"+strconv.Itoa(i)), 0o755)
				}
			})
			t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
			f.stubAction(t, "lockmany")
			snap := filepath.Join(t.TempDir(), "snap.txt")
			t.Setenv("CLAUSTRUM_GITSTUB_SNAP", snap)
			slowGit(t, "read-tree", "fail", 0, `fatal: synthetic checkout failure\n`, "")
			raw, _ := f.create(t, s, "w1", "", 0)
			order := readSnap(t, snap)
			i := slices.IndexFunc(order, func(n string) bool { return strings.HasPrefix(n, "r") })
			if i < 0 {
				t.Fatalf("stub snapshot %q holds no rN", order)
			}
			wantError(t, raw, "git worktree add failed (checkout): fatal: synthetic checkout failure"+
				fmt.Sprintf(stepA, f.leaf(), order[i]), "worktree_add_failed")
			want := slices.Sorted(slices.Values(order[i:]))
			if got := f.leafEntries(t); !slices.Equal(got, want) {
				t.Errorf("leaf entries = %q, want %q: the undo deletes in read order %q and stops at %s", got, want, order, order[i])
			}
			if got := f.regs(t); !slices.Equal(got, []string{"w1"}) {
				t.Errorf("registrations = %q, want w1 kept", got)
			}
			if !f.hasRef(t, "w1") {
				t.Errorf("refs/heads/w1 was deleted, want it kept after a step A failure")
			}
			told = order[i] != "r0"
		})
		if t.Failed() || told {
			return
		}
	}
	t.Skip("the directory read returned byte order in every run, so no run tells the two orders apart")
}

// TestWorktreeCreatePathSpelling pins how git.worktree_create treats the spelling
// of worktreePath. Both references create the worktree for a path with a trailing
// slash, "//" or "/./", and quote the path exactly as sent in the success frame
// and in the undo texts. The removeat part names the base name w1. Measured
// against f6010b97 and 90fca6e6 on Linux and macOS VMs.
func TestWorktreeCreatePathSpelling(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)
	const checkout = "git worktree add failed (checkout): fatal: synthetic checkout failure"
	const stepA = "; and the undo could not finish for %s: the worktree directory, its registration, and the branch all remain; remove them by hand before retrying (RemoveAll hsub: permission denied)"
	const stepC = "; and the undo could not finish for %s: the worktree directory remains (re-populated while undoing?); remove it by hand before retrying (removeat w1: permission denied)"
	spell := map[string]func(f wtFixture) string{
		"slash":  func(f wtFixture) string { return f.leaf() + "/" },
		"double": func(f wtFixture) string { return f.base + "/.claude//worktrees/w1" },
		"dot":    func(f wtFixture) string { return f.base + "/.claude/./worktrees/w1" },
	}
	for _, form := range []string{"slash", "double", "dot"} {
		for _, tc := range []struct{ name, action, undo string }{
			{"success", "", ""},
			{"undo finished", "", "none"},
			{"step A", "lock", stepA},
			{"step C", "lockparent", stepC},
		} {
			t.Run(form+" "+tc.name, func(t *testing.T) {
				f := newWTFixture(t, false)
				s := newTestServer(t)
				t.Cleanup(func() {
					_ = os.Chmod(filepath.Join(f.leaf(), "hsub"), 0o755)
					_ = os.Chmod(filepath.Dir(f.leaf()), 0o755)
				})
				path := spell[form](f)
				if tc.undo != "" {
					t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
					f.stubAction(t, tc.action)
					slowGit(t, "read-tree", "fail", 0, `fatal: synthetic checkout failure\n`, "")
				}
				raw, _ := f.createWith(t, s, "w1", 0, map[string]any{"worktreePath": path})
				switch tc.undo {
				case "":
					want := `"result":{"success":true,"path":` + jsonString(t, path) + `,"sourceBranch":"main","branch":"w1"}`
					if !strings.Contains(raw, want) {
						t.Fatalf("reply = %s\nwant %s", raw, want)
					}
					if _, err := os.Stat(filepath.Join(f.leaf(), "t.txt")); err != nil {
						t.Errorf("the worktree misses t.txt: %v", err)
					}
					if got := f.regs(t); !slices.Equal(got, []string{"w1"}) {
						t.Errorf("registrations = %q, want w1", got)
					}
				case "none":
					wantError(t, raw, checkout, "worktree_add_failed")
					if got := f.leafEntries(t); got != nil {
						t.Errorf("leaf entries = %q, want the leaf removed", got)
					}
					if f.hasRef(t, "w1") {
						t.Errorf("refs/heads/w1 exists, want it deleted")
					}
				default:
					wantError(t, raw, checkout+fmt.Sprintf(tc.undo, path), "worktree_add_failed")
				}
			})
		}
	}
}
