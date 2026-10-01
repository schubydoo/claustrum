//go:build unix

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// These tests cover the ancestor test of git.worktree_create with a worktreeRoot. A
// root is refused below a directory owned by a user other than you or uid 0. It is
// also refused below a directory writable by a shared group or by every user,
// without the sticky bit.
// Row names come from the side-by-side VM runs (issue 442). In the texts, <B> is
// the resolved temp dir of the test. A row named not_measured_ pins a choice that
// no capture measured.

const ancWriteTail = ", so they could replace what is beneath it; choose a location under " +
	"directories only you (or the system) control, or remove the extra write permission (chmod go-w)"

// ancWrite is the write text for root, naming dir.
func ancWrite(root, dir, who, mode string) string {
	return "refusing to create worktree: " + root + " passes through " + dir + ", which is writable by " +
		who + " without the sticky bit (mode " + mode + ")" + ancWriteTail
}

// ancOwner is the owner text for root, naming dir.
func ancOwner(root, dir string, uid uint32) string {
	return fmt.Sprintf("refusing to create worktree: %s passes through %s, which is owned by uid %d, "+
		"neither you nor the system; choose a location you reach through your own directories", root, dir, uid)
}

// sharedGroup makes the egid of the test a shared group: the passwd fixture has no
// line for the test user (row X1 of the root's own write test).
func sharedGroup(t *testing.T) {
	t.Helper()
	uid, gid := os.Geteuid(), os.Getegid()
	useAccountFiles(t, fmt.Sprintf("sgother:x:%d:%d:other:/home/sgother:/bin/sh\n", uid+1, gid+1),
		fmt.Sprintf("sgme:x:%d:\n", gid))
}

// privateGroup makes the egid of the test the private group of the test user, with
// the files of row L1_L10_private of the root's own write test.
func privateGroup(t *testing.T) {
	t.Helper()
	uid, gid := os.Geteuid(), os.Getegid()
	useAccountFiles(t, fmt.Sprintf("sgother:x:%d:%d:other:/home/sgother:/bin/sh\n", uid+1, gid+1)+
		fmt.Sprintf("sgme:x:%d:%d:me:/home/sgme:/bin/sh\n", uid, gid), fmt.Sprintf("sgme:x:%d:\n", gid))
}

// ancBase returns the temp dir of the test with its symlinks resolved and mode
// 0755. The walk names resolved paths, and on macOS the temp dir is under /var, a
// symlink to /private/var.
func ancBase(t *testing.T) string {
	t.Helper()
	b, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(b, 0o755); err != nil {
		t.Fatal(err)
	}
	return b
}

// ancDir makes dir with the egid of the test as its group, then sets mode. The mode
// goes back to 0755 at cleanup, so TempDir can delete the tree. A directory below
// dir must be made before a mode that denies the owner.
func ancDir(t *testing.T, dir string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, -1, os.Getegid()); err != nil {
		t.Skipf("cannot set the group of %s: %v", dir, err)
	}
	chmodForTest(t, dir, mode)
}

// ancLink makes the symlink link with target.
func ancLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// foreignOwner makes rootAncestorStat report uid as the owner of each path.
func foreignOwner(t *testing.T, uid uint32, paths ...string) {
	t.Helper()
	old := rootAncestorStat
	rootAncestorStat = func(p string) (uint32, uint32, fs.FileMode, error) {
		u, g, m, err := old(p)
		if slices.Contains(paths, p) {
			u = uid
		}
		return u, g, m, err
	}
	t.Cleanup(func() { rootAncestorStat = old })
}

func wantAncestor(t *testing.T, root, want string) {
	t.Helper()
	if got := worktreeRootAncestorRefusal(root); got != want {
		t.Errorf("worktreeRootAncestorRefusal(%q) =\n  %q\nwant\n  %q", root, got, want)
	}
}

// The judgement of one directory: owner first, then the sticky bit, then the group
// and other write bits. Rows P-M1 to P-M8 of the test list.
func TestRootAncestorFault(t *testing.T) {
	euid, egid := os.Geteuid(), os.Getegid()
	if u := uint32(euid); u == 65534 || u == 4294967294 {
		t.Skipf("the test user is uid %d, a foreign owner of these rows", u)
	}
	const writeFmt = "writable by %s without the sticky bit (mode %s), so they could replace what is " +
		"beneath it; choose a location under directories only you (or the system) control, or remove " +
		"the extra write permission (chmod go-w)"
	owner := func(uid uint32) string {
		return fmt.Sprintf("owned by uid %d, neither you nor the system; choose a location you reach "+
			"through your own directories", uid)
	}
	write := func(who, mode string) string { return fmt.Sprintf(writeFmt, who, mode) }
	me, gid := uint32(euid), uint32(egid)
	cases := []struct {
		name     string
		private  bool
		uid, gid uint32
		mode     fs.FileMode
		want     string
	}{
		// P-M1 (G9, G10).
		{name: "G9_root_owner", uid: 0, gid: 0, mode: 0o755},
		{name: "G9_daemon_user", uid: me, gid: gid, mode: 0o755},
		{name: "G10_foreign_linux", uid: 65534, gid: gid, mode: 0o755, want: owner(65534)},
		{name: "G10_foreign_macos_unsigned", uid: 4294967294, gid: gid, mode: 0o755, want: owner(4294967294)},
		// P-M1b (G31, G32): the owner test comes first, and the sticky bit does not clear it.
		{name: "G31_foreign_0777", uid: 65534, gid: gid, mode: 0o777, want: owner(65534)},
		{name: "G32_foreign_1755", uid: 65534, gid: gid, mode: 0o755 | fs.ModeSticky, want: owner(65534)},
		// P-M2 (G8, G28, G33a, G33b): uid 0 passes the owner test only.
		{name: "G8_root_0775", uid: 0, gid: 0, mode: 0o775, want: write("its group", "0775")},
		{name: "G33a_root_0757", uid: 0, gid: 0, mode: 0o757, want: write("every user on this host", "0757")},
		{name: "G33b_root_0777", uid: 0, gid: 0, mode: 0o777, want: write("its group and every user on this host", "0777")},
		// P-M4 (G33c): the private-group pass does not depend on the owner.
		{name: "G33c_root_private_0775", private: true, uid: 0, gid: gid, mode: 0o775},
		{name: "G1_private_0775", private: true, uid: me, gid: gid, mode: 0o775},
		{name: "K1_shared_0775", uid: me, gid: gid, mode: 0o775, want: write("its group", "0775")},
		// P-M5 (G2, G3).
		{name: "G2_0757", private: true, uid: me, gid: gid, mode: 0o757, want: write("every user on this host", "0757")},
		{name: "G3_linux_private_0777", private: true, uid: me, gid: gid, mode: 0o777, want: write("every user on this host", "0777")},
		{name: "G3_macos_shared_0777", uid: me, gid: gid, mode: 0o777, want: write("its group and every user on this host", "0777")},
		// M6 (G4, G5).
		{name: "G4_1777", uid: me, gid: gid, mode: 0o777 | fs.ModeSticky},
		{name: "G5_shared_1775", uid: me, gid: gid, mode: 0o775 | fs.ModeSticky},
		// P-M8 (G7, G36a, G36b): the permission bits only, four digits.
		{name: "G7_setgid_2775", uid: me, gid: gid, mode: 0o775 | fs.ModeSetgid, want: write("its group", "0775")},
		{name: "G36b_setuid_4775", uid: me, gid: gid, mode: 0o775 | fs.ModeSetuid, want: write("its group", "0775")},
		{name: "G36a_0070", uid: me, gid: gid, mode: 0o070, want: write("its group", "0070")},
		// M7 (G29, G38a, G38b).
		{name: "G29_0111", uid: me, gid: gid, mode: 0o111},
		{name: "G38b_0000", uid: me, gid: gid, mode: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.private {
				privateGroup(t)
			} else {
				sharedGroup(t)
			}
			if got := rootAncestorFault(c.uid, c.gid, c.mode, euid); got != c.want {
				t.Errorf("rootAncestorFault(%d, %d, %v) =\n  %q\nwant\n  %q", c.uid, c.gid, c.mode, got, c.want)
			}
		})
	}
}

// W-K0-control (K0, D3, M6): the stock chain of the host passes, "/" included. On
// Linux "/tmp" is 1777 and owned by root. A red result here is a fact of the host.
func TestRootAncestorStockChain(t *testing.T) {
	wantAncestor(t, filepath.Join(ancBase(t), "o"), "")
}

// W-M3-group (K1, G6): a shared group-writable directory refuses.
func TestRootAncestorGroupWritable(t *testing.T) {
	for _, mode := range []fs.FileMode{0o775, 0o770} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			sharedGroup(t)
			b := ancBase(t)
			g := filepath.Join(b, "G")
			ancDir(t, filepath.Join(g, "o"), 0o755)
			ancDir(t, g, mode)
			root := filepath.Join(g, "o")
			wantAncestor(t, root, ancWrite(root, g, "its group", fmt.Sprintf("%04o", mode)))
		})
	}
}

// W-M4-private (G1): a group-writable directory of the private group passes.
func TestRootAncestorPrivateGroup(t *testing.T) {
	privateGroup(t)
	g := filepath.Join(ancBase(t), "G")
	ancDir(t, g, 0o775)
	wantAncestor(t, filepath.Join(g, "o"), "")
}

// W-M5-world (G2, G3): the other-write bit refuses, and the group rule picks <who>.
func TestRootAncestorWorldWritable(t *testing.T) {
	for _, c := range []struct {
		name    string
		private bool
		mode    fs.FileMode
		who     string
	}{
		{"G2_0757", true, 0o757, "every user on this host"},
		{"G3_private_0777", true, 0o777, "every user on this host"},
		{"G3_shared_0777", false, 0o777, "its group and every user on this host"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.private {
				privateGroup(t)
			} else {
				sharedGroup(t)
			}
			g := filepath.Join(ancBase(t), "G")
			ancDir(t, g, c.mode)
			root := filepath.Join(g, "o")
			wantAncestor(t, root, ancWrite(root, g, c.who, fmt.Sprintf("%04o", c.mode)))
		})
	}
}

// W-M6-sticky (G4, G5): the sticky bit clears both mode tests.
func TestRootAncestorSticky(t *testing.T) {
	for _, mode := range []fs.FileMode{0o777 | fs.ModeSticky, 0o775 | fs.ModeSticky} {
		t.Run(fmt.Sprintf("%v", mode), func(t *testing.T) {
			sharedGroup(t)
			g := filepath.Join(ancBase(t), "G")
			ancDir(t, g, mode)
			wantAncestor(t, filepath.Join(g, "o"), "")
		})
	}
}

// W-M7-no-write (G29, G38a): a directory with no write bit for others passes, even
// when its owner cannot search or read it. The root below it exists.
func TestRootAncestorNoWriteBits(t *testing.T) {
	for _, c := range []struct {
		name string
		mode fs.FileMode
	}{{"G29_0111", 0o111}, {"G38a_0600", 0o600}} {
		t.Run(c.name, func(t *testing.T) {
			sharedGroup(t)
			p := filepath.Join(ancBase(t), "P")
			ancDir(t, filepath.Join(p, "o"), 0o755)
			ancDir(t, p, c.mode)
			wantAncestor(t, filepath.Join(p, "o"), "")
		})
	}
}

// W-D1-top-first (G11, G12, G35): the highest failing directory is named.
func TestRootAncestorTopFirst(t *testing.T) {
	for _, c := range []struct {
		name   string
		shared []string // under <B>, top first
		root   string
	}{
		{"G11", []string{"H"}, "H/G/o"},
		{"G12", []string{"H", "H/G"}, "H/G/o"},
		{"G35", []string{"H", "H/G", "H/G/K"}, "H/G/K/o"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sharedGroup(t)
			b := ancBase(t)
			root := filepath.Join(b, c.root)
			ancDir(t, filepath.Dir(root), 0o755)
			for i := len(c.shared) - 1; i >= 0; i-- {
				ancDir(t, filepath.Join(b, c.shared[i]), 0o775)
			}
			wantAncestor(t, root, ancWrite(root, filepath.Join(b, "H"), "its group", "0775"))
		})
	}
}

// W-D1-mixed-kinds (G34a, G34b): the kind of failure plays no part in the choice. A
// foreign owner comes from the stat seam.
func TestRootAncestorTopFirstMixedKinds(t *testing.T) {
	if os.Geteuid() == 65534 {
		t.Skip("the test user is uid 65534, the foreign owner of these rows")
	}
	t.Run("G34a_write_above_owner", func(t *testing.T) {
		sharedGroup(t)
		b := ancBase(t)
		h, g := filepath.Join(b, "H"), filepath.Join(b, "H", "G")
		ancDir(t, g, 0o755)
		ancDir(t, h, 0o775)
		foreignOwner(t, 65534, g)
		root := filepath.Join(g, "o")
		wantAncestor(t, root, ancWrite(root, h, "its group", "0775"))
	})
	t.Run("G34b_owner_above_write", func(t *testing.T) {
		sharedGroup(t)
		b := ancBase(t)
		h, g := filepath.Join(b, "H"), filepath.Join(b, "H", "G")
		ancDir(t, g, 0o775)
		foreignOwner(t, 65534, h)
		root := filepath.Join(g, "o")
		wantAncestor(t, root, ancOwner(root, h, 65534))
	})
}

// W-D2-root-excluded: the root itself is not in the walk. The root's own write test still sends
// its own text for a group-writable root. This rests on the older f6010b97
// measurement of the root's own write test.
func TestRootAncestorRootExcluded(t *testing.T) {
	sharedGroup(t)
	root := filepath.Join(ancBase(t), "R")
	ancDir(t, root, 0o775)
	wantAncestor(t, root, "")
	want := "refusing to create worktree: " + root + " is writable by its group (mode 0775); choose a " +
		"directory only you can write to, or remove the extra write permission (chmod go-w)"
	if got := worktreeRootShareRefusal(root); got != want {
		t.Errorf("worktreeRootShareRefusal = %q, want %q", got, want)
	}
}

// W-D5-missing (G13a, G13b): a missing level ends the walk, and every existing
// directory above it is judged. <root> is the absent root.
func TestRootAncestorMissingLevels(t *testing.T) {
	for _, c := range []struct{ name, root string }{{"G13a", "G/o"}, {"G13b", "G/n1/n2"}} {
		t.Run(c.name, func(t *testing.T) {
			sharedGroup(t)
			b := ancBase(t)
			g := filepath.Join(b, "G")
			ancDir(t, g, 0o775)
			root := filepath.Join(b, c.root)
			wantAncestor(t, root, ancWrite(root, g, "its group", "0775"))
		})
	}
}

// W-D7-judge-first (G36a): a group-writable directory that its owner cannot search
// is judged before the walk looks inside it.
func TestRootAncestorJudgedBeforeSearch(t *testing.T) {
	skipIfRoot(t)
	sharedGroup(t)
	g := filepath.Join(ancBase(t), "G")
	ancDir(t, filepath.Join(g, "o"), 0o755)
	ancDir(t, g, 0o070)
	root := filepath.Join(g, "o")
	wantAncestor(t, root, ancWrite(root, g, "its group", "0070"))
}

// W-G14-link-to-group (G14, G14c): a symlink is not judged itself. The directory it
// names is judged and named. <root> keeps the link.
func TestRootAncestorLinkToGroupDir(t *testing.T) {
	for _, c := range []struct{ name, target string }{{"G14_absolute", ""}, {"G14c_relative", "G"}} {
		t.Run(c.name, func(t *testing.T) {
			sharedGroup(t)
			b := ancBase(t)
			g := filepath.Join(b, "G")
			ancDir(t, g, 0o775)
			target := c.target
			if target == "" {
				target = g
			}
			ancLink(t, target, filepath.Join(b, "L"))
			root := filepath.Join(b, "L", "o")
			wantAncestor(t, root, ancWrite(root, g, "its group", "0775"))
		})
	}
}

// W-G14b-target-parent (G14b): the chain above a symlink target is judged.
func TestRootAncestorLinkTargetParent(t *testing.T) {
	sharedGroup(t)
	b := ancBase(t)
	g := filepath.Join(b, "G")
	ancDir(t, filepath.Join(g, "sub"), 0o755)
	ancDir(t, g, 0o775)
	ancLink(t, filepath.Join(g, "sub"), filepath.Join(b, "L"))
	root := filepath.Join(b, "L", "o")
	wantAncestor(t, root, ancWrite(root, g, "its group", "0775"))
}

// W-G14d-target (G14d): the symlink target itself is judged, in its resolved spelling.
func TestRootAncestorLinkTarget(t *testing.T) {
	sharedGroup(t)
	b := ancBase(t)
	g, sub := filepath.Join(b, "G"), filepath.Join(b, "G", "sub")
	ancDir(t, g, 0o755)
	ancDir(t, sub, 0o775)
	ancLink(t, sub, filepath.Join(b, "L"))
	root := filepath.Join(b, "L", "o")
	wantAncestor(t, root, ancWrite(root, sub, "its group", "0775"))
}

// W-G15-lexical (G15): a lexical directory above a symlink is judged, even though the
// resolved root does not pass through it.
func TestRootAncestorLexicalAboveLink(t *testing.T) {
	sharedGroup(t)
	b := ancBase(t)
	g := filepath.Join(b, "G")
	ancDir(t, filepath.Join(b, "safe"), 0o755)
	ancDir(t, g, 0o775)
	ancLink(t, filepath.Join(b, "safe"), filepath.Join(g, "s"))
	root := filepath.Join(g, "s", "o")
	wantAncestor(t, root, ancWrite(root, g, "its group", "0775"))
}

// W-G15b-target (G15b): a symlink below a safe directory leads to a group-writable one.
func TestRootAncestorLinkFromSafeDir(t *testing.T) {
	sharedGroup(t)
	b := ancBase(t)
	g, safe := filepath.Join(b, "G"), filepath.Join(b, "safe")
	ancDir(t, safe, 0o755)
	ancDir(t, g, 0o775)
	ancLink(t, g, filepath.Join(safe, "s"))
	root := filepath.Join(safe, "s", "o")
	wantAncestor(t, root, ancWrite(root, g, "its group", "0775"))
}

// W-G27g-resolved-name (G27g): a directory reached through a symlink is named in its
// resolved spelling.
func TestRootAncestorResolvedName(t *testing.T) {
	sharedGroup(t)
	b := ancBase(t)
	hg := filepath.Join(b, "H", "G")
	ancDir(t, filepath.Join(b, "P"), 0o755)
	ancDir(t, filepath.Join(b, "H"), 0o755)
	ancDir(t, hg, 0o775)
	ancLink(t, filepath.Join(b, "H"), filepath.Join(b, "P", "s"))
	root := filepath.Join(b, "P", "s", "G", "o")
	wantAncestor(t, root, ancWrite(root, hg, "its group", "0775"))
}

// W-G30-root-as-sent (G30a to G30c): <root> is the root as sent, and <dir> is clean.
func TestRootAncestorRootAsSent(t *testing.T) {
	sharedGroup(t)
	b := ancBase(t)
	q := filepath.Join(b, "Q")
	ancDir(t, filepath.Join(q, "o"), 0o755)
	ancDir(t, q, 0o775)
	for _, root := range []string{q + "/o/", q + "//o", q + "/./o"} {
		wantAncestor(t, root, ancWrite(root, q, "its group", "0775"))
	}
}

// W-stop-on-resolve-error (G37b, G39): a failed resolve ends the walk with no
// refusal. The root-chain step answers these rows.
func TestRootAncestorResolveErrorEndsWalk(t *testing.T) {
	sharedGroup(t)
	b := ancBase(t)
	ancLink(t, filepath.Join(b, "nonexistent"), filepath.Join(b, "G"))
	wantAncestor(t, filepath.Join(b, "G", "o"), "")
	ancLink(t, "L2", filepath.Join(b, "L"))
	ancLink(t, "L", filepath.Join(b, "L2"))
	wantAncestor(t, filepath.Join(b, "L", "o"), "")
}

// G42 (Linux and macOS VMs, 89cb6289 only): a root that is itself a symlink. The
// chain above its target is judged. <root> is the symlink as sent, and <dir> the
// failing resolved directory.
func TestRootAncestorSymlinkedRoot(t *testing.T) {
	sharedGroup(t)
	b := ancBase(t)
	s := filepath.Join(b, "S")
	ancDir(t, filepath.Join(s, "T"), 0o755)
	ancDir(t, s, 0o775)
	ancLink(t, filepath.Join(s, "T"), filepath.Join(b, "R"))
	root := filepath.Join(b, "R")
	wantAncestor(t, root, ancWrite(root, s, "its group", "0775"))
}

// Choices that no capture measured.
func TestRootAncestorNotMeasured(t *testing.T) {
	// A failing directory above a symlink loop is reached first and refuses.
	t.Run("not_measured_failing_dir_above_loop", func(t *testing.T) {
		sharedGroup(t)
		b := ancBase(t)
		h := filepath.Join(b, "H")
		ancDir(t, h, 0o755)
		ancLink(t, "L2", filepath.Join(h, "L"))
		ancLink(t, "L", filepath.Join(h, "L2"))
		chmodForTest(t, h, 0o775)
		root := filepath.Join(h, "L", "o")
		wantAncestor(t, root, ancWrite(root, h, "its group", "0775"))
	})
	// A group-writable regular file above the root is judged like a directory.
	t.Run("not_measured_group_writable_file", func(t *testing.T) {
		sharedGroup(t)
		b := ancBase(t)
		f := filepath.Join(b, "F")
		writeFile(t, f, "", 0o644)
		if err := os.Chown(f, -1, os.Getegid()); err != nil {
			t.Skipf("cannot set the group of %s: %v", f, err)
		}
		chmodForTest(t, f, 0o664)
		root := filepath.Join(f, "o")
		wantAncestor(t, root, ancWrite(root, f, "its group", "0664"))
	})
	// An ancestor that cannot be searched, with more levels below it: the resolve of
	// the next level fails, and the walk ends with no refusal. The group-writable K
	// below it is not judged.
	t.Run("not_measured_unsearchable_with_levels_below", func(t *testing.T) {
		skipIfRoot(t)
		sharedGroup(t)
		g := filepath.Join(ancBase(t), "G")
		ancDir(t, filepath.Join(g, "K", "o"), 0o755)
		ancDir(t, filepath.Join(g, "K"), 0o775)
		ancDir(t, g, 0o600)
		wantAncestor(t, filepath.Join(g, "K", "o"), "")
	})
}

// ancFixture is a temp dir <B> with the repository <B>/T.
type ancFixture struct{ B, T string }

func newAncFixture(t *testing.T) *ancFixture {
	t.Helper()
	requireGit(t)
	b := ancBase(t)
	requireTempOutsideCheckout(t, b)
	f := &ancFixture{B: b, T: filepath.Join(b, "T")}
	r0Repo(t, f.T)
	return f
}

func (f *ancFixture) p(rel string) string { return filepath.Join(f.B, rel) }

// create sends git.worktree_create with branch c1.
func (f *ancFixture) create(t *testing.T, base, root, wp string) string {
	t.Helper()
	params := map[string]any{"baseRepo": base, "branchName": "c1", "worktreePath": wp}
	if root != "" {
		params["worktreeRoot"] = root
	}
	return dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create", params))
}

func ancFrame(t *testing.T, text, code string) string {
	return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"` + jsonEscape(t, text) +
		`","errorCode":"` + code + `"}}`
}

func ancSuccess(t *testing.T, wp string) string {
	return `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":` + jsonString(t, wp) +
		`,"sourceBranch":"main","branch":"c1"}}`
}

func wantFrame(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("reply =\n  %s\nwant\n  %s", got, want)
	}
}

// D-K1-nothing-created and D-nine-calls (K1): the refusal comes after the 9 git
// calls, runs no `worktree add`, and creates nothing.
func TestWorktreeCreateRootAncestorRefused(t *testing.T) {
	sharedGroup(t)
	f := newAncFixture(t)
	g, root := f.p("G"), f.p("G/o")
	ancDir(t, root, 0o755)
	ancDir(t, g, 0o775)
	before := r0cState(t, &r0Fixture{T: f.T})
	calls := logGitArgv(t)
	got := f.create(t, f.T, root, filepath.Join(root, "cp", "c1"))
	wantFrame(t, got, ancFrame(t, ancWrite(root, g, "its group", "0775"), "unsafe_path"))
	if got := r0cCalls(calls()); strings.Join(got, "\n") != strings.Join(r0cCreateCalls, "\n") {
		t.Errorf("git calls = %q, want %q", got, r0cCreateCalls)
	}
	if _, err := os.Lstat(filepath.Join(root, "cp")); err == nil {
		t.Errorf("%s/cp exists; a refusal creates nothing", root)
	}
	if after := r0cState(t, &r0Fixture{T: f.T}); after != before || branchExists(t, f.T, "c1") {
		t.Errorf("entries and branches = %s, want %s; a refusal creates nothing", after, before)
	}
}

// D-G16-before-root (G16): with both the ancestor and the root group-writable, the
// ancestor text wins over the text of the root's own write test.
func TestWorktreeCreateRootAncestorBeforeRootTest(t *testing.T) {
	sharedGroup(t)
	f := newAncFixture(t)
	g, root := f.p("G"), f.p("G/o")
	ancDir(t, root, 0o775)
	ancDir(t, g, 0o775)
	wantFrame(t, f.create(t, f.T, root, filepath.Join(root, "cp", "c1")),
		ancFrame(t, ancWrite(root, g, "its group", "0775"), "unsafe_path"))
}

// D-G18-G40-before-chain (G18, G40a, G40b): the ancestor text comes before the
// .git test of the root-chain step, even at the same level.
func TestWorktreeCreateRootAncestorBeforeGitChain(t *testing.T) {
	for _, c := range []struct {
		name, root string
		setup      func(t *testing.T, f *ancFixture)
	}{
		{"G18_repo_below", "G/X/o", func(t *testing.T, f *ancFixture) { runGit(t, f.p("G/X"), "init", "-q") }},
		{"G40a_repo_in_root", "G/o", func(t *testing.T, f *ancFixture) { runGit(t, f.p("G/o"), "init", "-q") }},
		{"G40b_git_file_in_G", "G/o", func(t *testing.T, f *ancFixture) { writeFile(t, f.p("G/.git"), "", 0o644) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			sharedGroup(t)
			f := newAncFixture(t)
			root := f.p(c.root)
			ancDir(t, root, 0o755)
			c.setup(t, f)
			ancDir(t, f.p("G"), 0o775)
			wantFrame(t, f.create(t, f.T, root, filepath.Join(root, "cp", "c1")),
				ancFrame(t, ancWrite(root, f.p("G"), "its group", "0775"), "unsafe_path"))
		})
	}
}

// Rows Y11d and T7 of docs/PROTOCOL.md: the ancestor text comes before the checkout
// tests. The root leads into the repository <B>/G/T through the symlink <B>/L, and
// <B>/G is group-writable.
func TestWorktreeCreateRootAncestorBeforeCheckout(t *testing.T) {
	sharedGroup(t)
	f := newAncFixture(t)
	g, repo := f.p("G"), f.p("G/T")
	r0Repo(t, repo)
	ancDir(t, g, 0o775)
	ancLink(t, repo, f.p("L"))
	root := f.p("L/.claude")
	wantFrame(t, f.create(t, repo, root, filepath.Join(root, "cp", "c1")),
		ancFrame(t, ancWrite(root, g, "its group", "0775"), "unsafe_path"))
}

// D-G36a-before-chain (G36a): an unsearchable group-writable ancestor gets the
// ancestor text, not the lstat text of the root-chain step.
func TestWorktreeCreateRootAncestorUnsearchable(t *testing.T) {
	skipIfRoot(t)
	sharedGroup(t)
	f := newAncFixture(t)
	g, root := f.p("G"), f.p("G/o")
	ancDir(t, root, 0o755)
	ancDir(t, g, 0o070)
	wantFrame(t, f.create(t, f.T, root, filepath.Join(root, "cp", "c1")),
		ancFrame(t, ancWrite(root, g, "its group", "0070"), "unsafe_path"))
}

// D-chain-texts-kept (G37a, G37b, G38a, G39): with no failing ancestor, the
// root-chain step answers as before.
func TestWorktreeCreateRootAncestorKeepsChainTexts(t *testing.T) {
	for _, c := range []struct {
		name   string
		root   bool // skip as root: the row denies the owner by mode
		setup  func(t *testing.T, f *ancFixture)
		rootAt string
		text   string
		code   string
	}{
		{name: "G37a_regular_file", rootAt: "G/o",
			setup: func(t *testing.T, f *ancFixture) { writeFile(t, f.p("G"), "", 0o644) },
			text:  "failed to create parent directory: <B>/G: not a directory", code: "mkdir_failed"},
		{name: "G37b_dangling_link", rootAt: "G/o",
			setup: func(t *testing.T, f *ancFixture) { ancLink(t, f.p("nonexistent"), f.p("G")) },
			text:  "failed to create parent directory: lstat <B>/nonexistent: no such file or directory", code: "mkdir_failed"},
		{name: "G38a_0600", root: true, rootAt: "G/o",
			setup: func(t *testing.T, f *ancFixture) {
				ancDir(t, f.p("G/o"), 0o755)
				ancDir(t, f.p("G"), 0o600)
			},
			text: "failed to create parent directory: lstat <B>/G/.git: permission denied", code: "mkdir_failed"},
		{name: "G39_loop", rootAt: "L/o",
			setup: func(t *testing.T, f *ancFixture) {
				ancLink(t, "L2", f.p("L"))
				ancLink(t, "L", f.p("L2"))
			},
			text: "refusing to create worktree: <B>/L/o passes through too many symbolic links (a loop?)", code: "unsafe_path"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.root {
				skipIfRoot(t)
			}
			sharedGroup(t)
			f := newAncFixture(t)
			c.setup(t, f)
			root := f.p(c.rootAt)
			wantFrame(t, f.create(t, f.T, root, filepath.Join(root, "cp", "c1")),
				ancFrame(t, strings.ReplaceAll(c.text, "<B>", f.B), c.code))
		})
	}
}

// D-G41-dotdot-first (G41a): the ".." refusal comes 6 git calls before the ancestor
// test.
func TestWorktreeCreateRootAncestorAfterDotDot(t *testing.T) {
	sharedGroup(t)
	f := newAncFixture(t)
	ancDir(t, f.p("G"), 0o775)
	root := f.B + "/G/../G/o"
	wantFrame(t, f.create(t, f.T, root, root+"/cp/c1"), ancFrame(t, "refusing to create worktree: "+root+
		` contains a ".." component; choose the worktree location by its absolute path, without "..", `+
		"beneath the filesystem root", "unsafe_path"))
}

// D-G20-base-chain (G20): the chain of baseRepo is not walked.
func TestWorktreeCreateRootAncestorIgnoresRepoChain(t *testing.T) {
	sharedGroup(t)
	f := newAncFixture(t)
	repo := f.p("G/T")
	r0Repo(t, repo)
	ancDir(t, f.p("G"), 0o775)
	root := f.p("safe/o")
	ancDir(t, root, 0o755)
	wp := filepath.Join(root, "cp", "c1")
	wantFrame(t, f.create(t, repo, root, wp), ancSuccess(t, wp))
}

// D-G21-default (G21, G22): without a worktreeRoot no ancestor test runs, even with
// the repository and the directory above it group-writable.
func TestWorktreeCreateDefaultSkipsRootAncestor(t *testing.T) {
	sharedGroup(t)
	f := newAncFixture(t)
	repo := f.p("G/T")
	r0Repo(t, repo)
	ancDir(t, repo, 0o775)
	ancDir(t, f.p("G"), 0o775)
	wp := filepath.Join(repo, ".claude", "worktrees", "c1")
	wantFrame(t, f.create(t, repo, "", wp), ancSuccess(t, wp))
}

// D-G25-remove (G25): git.worktree_remove runs no ancestor test.
func TestWorktreeRemoveSkipsRootAncestor(t *testing.T) {
	sharedGroup(t)
	f := newAncFixture(t)
	root := f.p("G/o")
	leaf := filepath.Join(root, "cp", "w1")
	dirtyWorktree(t, f.T, leaf, "wt1")
	ancDir(t, f.p("G"), 0o775)
	got := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": f.T, "worktreePath": leaf, "worktreeRoot": root}))
	wantFrame(t, got, `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`)
	if _, err := os.Lstat(leaf); err == nil {
		t.Errorf("%s is still there, but all three remove it", leaf)
	}
}
