//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// An external worktreeRoot writable by every user on the host is refused (mode is
// echoed). Measured against 7d193f89. The asserted substring is the suffix common
// to the world-only and group+world spellings. This test reads the host's own
// passwd and group files. If they do not show the temp dir's group as private,
// the text adds "its group and ", and the test still passes. TestWorktreeRootShareRefusalGroupRule pins the group rule with fixture
// files.
func TestWorktreeCreateExternalWorldWritable(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	requireTempOutsideCheckout(t, base)
	repo := filepath.Join(base, "R")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := filepath.Join(base, "wld")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "w",
			"worktreePath": filepath.Join(root, "d", "wt"), "worktreeRoot": root}))
	if !strings.Contains(raw, `"errorCode":"unsafe_path"`) ||
		!strings.Contains(raw, "every user on this host (mode 0777)") {
		t.Errorf("create with a world-writable root = %s, want the writable refusal", raw)
	}
}

// The uid-ownership refusal is the first check in the reference's order and needs a
// root the daemon user does not own — the filesystem root is the one such directory
// present on every unix host. worktreeRootShareRefusal only stats, so this touches
// nothing. The ownership is asserted first so a host where "/" is somehow ours skips
// rather than reporting a false negative.
func TestWorktreeRootShareRefusalForeignOwner(t *testing.T) {
	skipIfRoot(t)
	root := string(os.PathSeparator)
	fi, err := os.Stat(root)
	if err != nil {
		t.Skipf("cannot stat %s: %v", root, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == os.Geteuid() {
		t.Skipf("%s is owned by the test user; the foreign-owner arm is unreachable here", root)
	}
	want := fmt.Sprintf("is owned by uid %d, not by you (uid %d)", st.Uid, os.Geteuid())
	if got := worktreeRootShareRefusal(root); !strings.Contains(got, want) {
		t.Errorf("worktreeRootShareRefusal(%s) = %q, want it to contain %q", root, got, want)
	}
}

// The symlinked-<directory> refusal gates REMOVE as well as create (the create arm
// is covered by TestWorktreeCreateExternalSpellingAndSymlink): a planted link at the
// <directory> level must not carry the removal out of the worktree location.
func TestWorktreeRemoveExternalDirSymlink(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	repo := filepath.Join(base, "R")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	root := filepath.Join(base, "mine")
	outside := filepath.Join(base, "outside", "wt")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "outside"), filepath.Join(root, "dlink")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": filepath.Join(root, "dlink", "wt"), "worktreeRoot": root}))
	if !strings.Contains(worktreeErrorField(t, raw), "is a symbolic link; the directory under the worktree location must be a real directory") {
		t.Errorf("remove through a symlinked <dir> = %s, want the symlink refusal", raw)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the removal followed the symlink out of the root (%v)", err)
	}
}

// A worktreePath that ends in a slash, with a worktreeRoot. The <directory> tests
// read the cleaned path, so "R/proj/w1/" is judged at "R/proj", not at "R/proj/w1".
// The frames were measured against f6010b97 and 90fca6e6 on Linux and macOS VMs
// (rows XS_c_slash, XS_c_slash_exists, XS2_c_slash_twice). Without the clean, the
// first case created the worktree in R/proj and wrote the marker there, and the
// others named the wrong directory.
func TestWorktreeExternalTrailingSlash(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	requireTempOutsideCheckout(t, base)
	repo := filepath.Join(base, "T")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := filepath.Join(base, "R")
	proj := filepath.Join(root, "proj")
	for _, d := range []string{filepath.Join(proj, "e0"), filepath.Join(proj, "p0")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(proj, "p0", "keep.txt"), "keep\n", 0o644)
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	create := func(wp string) string {
		t.Helper()
		return dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": repo, "branchName": "w1", "worktreePath": wp, "worktreeRoot": root}))
	}
	notEmpty := "refusing to create worktree: " + proj + " already exists, is not marked as a " +
		"worktree directory, and holds other files (for example \"e0\"); the per-repository " +
		"directory under a worktree location must start out empty — remove it, restore " +
		"its .claude-managed-worktrees file if you deleted it, or choose another location"

	t.Run("new leaf in a non-empty directory", func(t *testing.T) {
		wantError(t, create(proj+"/w1/"), notEmpty, "unsafe_path")
		if _, err := os.Lstat(filepath.Join(proj, "w1")); !os.IsNotExist(err) {
			t.Errorf("w1 was created (err=%v)", err)
		}
		if _, err := os.Lstat(filepath.Join(proj, managedWorktreesMarker)); !os.IsNotExist(err) {
			t.Errorf("the marker was written into %s (err=%v)", proj, err)
		}
	})
	t.Run("existing plain folder", func(t *testing.T) {
		wantError(t, create(proj+"/p0/"), notEmpty, "unsafe_path")
	})
	t.Run("second create in a fresh directory", func(t *testing.T) {
		wp := filepath.Join(root, "cp", "w1")
		raw := create(wp + "/")
		want := `{"success":true,"path":` + jsonString(t, wp+"/") + `,`
		if !strings.Contains(raw, want) {
			t.Fatalf("first create = %s, want %s", raw, want)
		}
		wantError(t, create(wp+"/"), "refusing to create worktree: "+wp+
			" already exists, and a new worktree is only ever created in a fresh directory", "unsafe_path")
	})
	t.Run("symlinked directory", func(t *testing.T) {
		outside := filepath.Join(base, "outside")
		if err := os.MkdirAll(filepath.Join(outside, "wt"), 0o755); err != nil {
			t.Fatal(err)
		}
		dlink := filepath.Join(root, "dlink")
		if err := os.Symlink(outside, dlink); err != nil {
			t.Fatal(err)
		}
		const tail = " is a symbolic link; the directory under the worktree location must be a real directory"
		wantError(t, create(dlink+"/w1/"), "refusing to create worktree: "+dlink+tail, "unsafe_path")
		if _, err := os.Lstat(filepath.Join(outside, "w1")); !os.IsNotExist(err) {
			t.Errorf("the create followed the symlink out of the root (err=%v)", err)
		}
		rm := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
			map[string]any{"baseRepo": repo, "worktreePath": dlink + "/wt/", "worktreeRoot": root}))
		if got := worktreeErrorField(t, rm); got != "refusing to remove worktree: "+dlink+tail {
			t.Errorf("remove through a symlinked <dir> = %s, want the symlink refusal", rm)
		}
	})
}

// writableWho names who beyond the owner can write. The three spellings are the
// reference's; the combined case is not reachable in the RPC test above without a
// shared group AND world-write, so it is pinned directly.
func TestWritableWho(t *testing.T) {
	cases := []struct {
		group, world bool
		want         string
	}{
		{true, true, "its group and every user on this host"},
		{false, true, "every user on this host"},
		{true, false, "its group"},
		{false, false, ""},
	}
	for _, c := range cases {
		if got := writableWho(c.group, c.world); got != c.want {
			t.Errorf("writableWho(group=%v,world=%v) = %q, want %q", c.group, c.world, got, c.want)
		}
	}
}

// worktreeRootShareRefusal returns "" for a missing root — the create fails later at
// the parent-creation step, not here.
func TestWorktreeRootShareRefusalMissingRoot(t *testing.T) {
	if got := worktreeRootShareRefusal(filepath.Join(t.TempDir(), "nonexistent")); got != "" {
		t.Errorf("missing root = %q, want no refusal", got)
	}
}

// useAccountFiles points the flat passwd and group seams at fixture files with the
// given content, and restores them at cleanup. A test that calls it must not run in
// parallel, since every worktreeRoot create reads the seams.
func useAccountFiles(t *testing.T, passwd, group string) {
	t.Helper()
	dir := t.TempDir()
	pw, gr := filepath.Join(dir, "passwd"), filepath.Join(dir, "group")
	writeFile(t, pw, passwd, 0o644)
	writeFile(t, gr, group, 0o644)
	oldPW, oldGR := flatPasswdPath, flatGroupPath
	flatPasswdPath, flatGroupPath = pw, gr
	t.Cleanup(func() { flatPasswdPath, flatGroupPath = oldPW, oldGR })
}

// groupRoot makes a root owned by the test user, with group gid and mode perm.
func groupRoot(t *testing.T, gid int, perm os.FileMode) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "R")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(root, -1, gid); err != nil {
		t.Skipf("cannot set the group of the root to %d: %v", gid, err)
	}
	chmodForTest(t, root, perm)
	return root
}

// A group-writable worktreeRoot counts as private only when the flat passwd and
// group files show the group as the daemon user's own. The fixture files carry the
// euid and egid of the test, so the host's own files play no part. A row named
// with an L, M, P, X or VR number follows that measured row (f6010b97, Linux and macOS
// VMs). A row named not_measured_ pins a choice that no capture measured.
func TestWorktreeRootShareRefusalGroupRule(t *testing.T) {
	uid, gid := os.Geteuid(), os.Getegid()
	me := fmt.Sprintf("sgme:x:%d:%d:me:/home/sgme:/bin/sh\n", uid, gid)
	other := fmt.Sprintf("sgother:x:%d:%d:other:/home/sgother:/bin/sh\n", uid+1, gid+1)
	otherSameGID := fmt.Sprintf("sgother:x:%d:%d:other:/home/sgother:/bin/sh\n", uid+1, gid)
	meElsewhere := fmt.Sprintf("sgme:x:%d:%d:me:/home/sgme:/bin/sh\n", uid, gid+1)
	grp := func(name, members string) string { return fmt.Sprintf("%s:x:%d:%s\n", name, gid, members) }
	refusal := func(root, who, mode string) string {
		return "refusing to create worktree: " + root + " is writable by " + who + " (mode " + mode +
			"); choose a directory only you can write to, or remove the extra write permission (chmod go-w)"
	}
	cases := []struct {
		name          string
		passwd, group string
		fileState     string // "missing passwd", "missing group" or "unreadable group"
		perm          os.FileMode
		who           string // "" means accepted
	}{
		{name: "L1_L10_private", passwd: other + me, group: grp("sgme", ""), perm: 0o775},
		{name: "L1b_M11_M14_user_listed", passwd: me + other, group: grp("sgme", "sgme"), perm: 0o775},
		{name: "L1_private_among_other_groups", passwd: me, group: fmt.Sprintf("sgother:x:%d:sgme\n", gid+1) + grp("sgme", ""), perm: 0o775},
		{name: "L5_other_member", passwd: me, group: grp("sgme", "sgother"), perm: 0o775, who: "its group"},
		{name: "M16_other_member", passwd: me, group: grp("sgme", "sgme,sgother"), perm: 0o775, who: "its group"},
		{name: "L5b_other_primary_user", passwd: me + otherSameGID, group: grp("sgme", ""), perm: 0o775, who: "its group"},
		{name: "M15_other_primary_user", passwd: me + otherSameGID, group: grp("sgme", "sgme"), perm: 0o775, who: "its group"},
		{name: "X1_no_passwd_line", passwd: other, group: grp("sgme", ""), perm: 0o775, who: "its group"},
		{name: "P11_only_gid_line_is_another_user", passwd: otherSameGID + meElsewhere, group: grp("sgme", "sgme"), perm: 0o775, who: "its group"},
		{name: "P11b_only_gid_line_is_another_user", passwd: otherSameGID + meElsewhere, group: grp("sgme", ""), perm: 0o775, who: "its group"},
		{name: "P12_group_named_after_the_other_user", passwd: otherSameGID + meElsewhere, group: grp("sgother", ""), perm: 0o775, who: "its group"},
		{name: "L9_group_renamed", passwd: me, group: grp("sgpriv", ""), perm: 0o775, who: "its group"},
		{name: "L13_no_user_with_that_primary_gid", passwd: meElsewhere, group: grp("sgme", "sgme"), perm: 0o775, who: "its group"},
		{name: "X2_no_group_line", passwd: me, group: "", perm: 0o775, who: "its group"},
		{name: "L2_private_0777", passwd: me, group: grp("sgme", ""), perm: 0o777, who: "every user on this host"},
		{name: "M4_shared_0777", passwd: other, group: grp("sgme", ""), perm: 0o777, who: "its group and every user on this host"},
		{name: "P1_passwd_comment_line", passwd: fmt.Sprintf("#sgme:x:%d:%d::/home/sgme:/bin/bash\n", uid, gid) + me, group: grp("sgme", ""), perm: 0o775},
		{name: "P2_group_comment_line", passwd: me, group: fmt.Sprintf("#old:x:%d:\n", gid) + grp("sgme", ""), perm: 0o775},
		{name: "P3_group_line_crlf", passwd: me, group: fmt.Sprintf("sgme:x:%d:\r\n", gid), perm: 0o775},
		{name: "P4_passwd_6_fields", passwd: fmt.Sprintf("sgme:x:%d:%d::/home/sgme\n", uid, gid), group: grp("sgme", ""), perm: 0o775},
		{name: "P5_passwd_8_fields", passwd: fmt.Sprintf("sgme:x:%d:%d::/home/sgme:/bin/bash:extra\n", uid, gid), group: grp("sgme", ""), perm: 0o775},
		{name: "P6_member_leading_space", passwd: me, group: grp("sgme", " sgme"), perm: 0o775},
		{name: "P7_plus_line_is_a_second_user", passwd: me + fmt.Sprintf("+sgme:x:%d:%d:::\n", uid, gid), group: grp("sgme", ""), perm: 0o775, who: "its group"},
		{name: "P8_second_gid_line_after_with_member", passwd: me, group: grp("sgme", "") + fmt.Sprintf("sgdup:x:%d:nobody\n", gid), perm: 0o775, who: "its group"},
		{name: "P9_second_gid_line_before", passwd: me, group: fmt.Sprintf("sgdup:x:%d:\n", gid) + grp("sgme", ""), perm: 0o775, who: "its group"},
		{name: "P10_passwd_name_leading_space", passwd: fmt.Sprintf(" sgme:x:%d:%d::/home/sgme:/bin/bash\n", uid, gid), group: grp("sgme", ""), perm: 0o775, who: "its group"},
		{name: "X3_second_gid_line_after", passwd: me, group: grp("sgme", "") + fmt.Sprintf("sgdup:x:%d:\n", gid), perm: 0o775, who: "its group"},
		{name: "VR1_VR2_shared_0755", passwd: other, group: grp("sgme", ""), perm: 0o755},
		{name: "not_measured_malformed_lines_skipped",
			passwd: "junk\n" + fmt.Sprintf("sgx:x:nan:%d:::\n", gid) + me,
			group:  "junk\nsgx:x:nan:\n" + grp("sgme", ""), perm: 0o775},
		{name: "not_measured_missing_group_file", passwd: me, fileState: "missing group", perm: 0o775, who: "its group"},
		{name: "not_measured_unreadable_group_file", passwd: me, group: grp("sgme", ""), fileState: "unreadable group", perm: 0o775, who: "its group"},
		{name: "not_measured_missing_passwd_file", passwd: me, group: grp("sgme", ""), fileState: "missing passwd", perm: 0o775, who: "its group"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			useAccountFiles(t, c.passwd, c.group)
			switch c.fileState {
			case "missing passwd":
				flatPasswdPath = filepath.Join(t.TempDir(), "missing")
			case "missing group":
				flatGroupPath = filepath.Join(t.TempDir(), "missing")
			case "unreadable group":
				// uid 0 reads a 0o000 file, so the fixture cannot deny it there.
				skipIfRoot(t)
				chmodForTest(t, flatGroupPath, 0o000)
			}
			root := groupRoot(t, gid, c.perm)
			want := ""
			if c.who != "" {
				want = refusal(root, c.who, fmt.Sprintf("%04o", c.perm))
			}
			if got := worktreeRootShareRefusal(root); got != want {
				t.Errorf("worktreeRootShareRefusal = %q, want %q", got, want)
			}
		})
	}

	// L12, M13: the group is the user's private group in the files, but it is
	// not the daemon's gid. That needs a supplementary group of the test user.
	t.Run("L12_M13_not_the_daemon_gid", func(t *testing.T) {
		groups, err := os.Getgroups()
		if err != nil {
			t.Skipf("getgroups: %v", err)
		}
		alt := -1
		for _, g := range groups {
			if g != gid {
				alt = g
				break
			}
		}
		if alt < 0 {
			t.Skip("the test user has no supplementary group")
		}
		useAccountFiles(t, fmt.Sprintf("sgme:x:%d:%d:me:/home/sgme:/bin/sh\n", uid, alt),
			fmt.Sprintf("sgme:x:%d:\n", alt))
		root := groupRoot(t, alt, 0o775)
		want := refusal(root, "its group", "0775")
		if got := worktreeRootShareRefusal(root); got != want {
			t.Errorf("worktreeRootShareRefusal = %q, want %q", got, want)
		}
	})
}
