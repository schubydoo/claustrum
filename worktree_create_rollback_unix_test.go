//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// These tests pin three parts of git.worktree_create on Linux and macOS: the step
// that removes a stale registration before the add, the refusal for a stale entry
// that stayed, and the registration that a rollback removes. Cells A and B are
// those of 89cb6289 on a Linux VM with git 2.43, 2 runs each. Cells Z15, Z16 and
// Z18 are those of 89cb6289 on Linux and macOS VMs. Every fixture is under
// t.TempDir, and no test gives a path outside it to a delete.

// staleRegFiles are the files of the old registration of the A cells, in the order
// that the Linux trace of 89cb6289 removes them.
var staleRegFiles = []string{"gitdir", "index", "logs/HEAD", "ORIG_HEAD", "commondir", "HEAD"}

// pathsBelow lists every path below dir, sorted, or nil when dir is gone.
func pathsBelow(t *testing.T, dir string) []string {
	t.Helper()
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return nil
	}
	out := []string{}
	err := filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel, _ := filepath.Rel(dir, p); rel != "." {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// TestDropStaleWorktreeRegistrationRecords pins the step before the add, one case
// for each record of the A cells. <L> is the real path of the leaf.
//
//   - Removed: A1 to A5c, A7 (a relative record), A8 (the entry has another name)
//     and A12c (the request goes through a symlink, the record is real).
//   - Kept, and not remembered: A6 and A6b (no .git part), A11 (a live worktree at
//     another path) and A12b (the record goes through a symlink, the request is
//     real).
//   - Kept, and remembered: A9 and A9b (a `locked` file), A10 (the entry has mode
//     0500, and only logs/HEAD goes) and A10b (the registrations folder has mode
//     0555, the files go and the empty folder stays). The mode cells need a user
//     that is not root.
//
// The last case is not a cell. HOME is a folder in the stale entry, so the home
// guard keeps the entry (claustrum's own guard, D2).
func TestDropStaleWorktreeRegistrationRecords(t *testing.T) {
	all := append([]string{"logs"}, staleRegFiles...)
	slices.Sort(all)
	for _, tc := range []struct {
		name, entry string
		record      func(root, leaf string) string
		locked      bool
		viaLink     bool   // the request names the repository through a symlink
		chmod       string // "entry" (0500) or "registry" (0555)
		home        bool   // HOME is a folder in the entry
		want        []string
		remembered  bool
	}{
		{name: "A1", record: func(_, l string) string { return l + "/.git/" }},
		{name: "A2", record: func(_, l string) string { return l + "/.git/\n" }},
		{name: "A3", record: func(_, l string) string { return l + "/.git\n" }},
		{name: "A4", record: func(_, l string) string { return l + "/.git" }},
		{name: "A5", record: func(_, l string) string { return l + "/.git//" }},
		{name: "A5b", record: func(_, l string) string { return l + "/.git/." }},
		{name: "A5c", record: func(_, l string) string { return l + "/.git \n" }},
		{name: "A6", record: func(_, l string) string { return l + "\n" }, want: all},
		{name: "A6b", record: func(_, l string) string { return l + "/" }, want: all},
		{name: "A7", record: func(string, string) string { return "../../../.claude/worktrees/w1/.git\n" }},
		{name: "A8", entry: "old9", record: func(_, l string) string { return l + "/.git/" }},
		{name: "A9", record: func(_, l string) string { return l + "/.git/" }, locked: true,
			want: append([]string{"locked"}, all...), remembered: true},
		{name: "A9b", record: func(_, l string) string { return l + "/.git\n" }, locked: true,
			want: append([]string{"locked"}, all...), remembered: true},
		{name: "A10", record: func(_, l string) string { return l + "/.git/" }, chmod: "entry",
			want: []string{"HEAD", "ORIG_HEAD", "commondir", "gitdir", "index", "logs"}, remembered: true},
		{name: "A10b", record: func(_, l string) string { return l + "/.git/" }, chmod: "registry",
			want: []string{}, remembered: true},
		{name: "A11", record: func(r, _ string) string { return r + "/live/w1/.git\n" }, want: all},
		{name: "A12b", record: func(r, _ string) string { return r + "/Tlink/.claude/worktrees/w1/.git/" }, want: all},
		{name: "A12c", record: func(_, l string) string { return l + "/.git/" }, viaLink: true},
		{name: "home in the entry (claustrum's guard, not measured)", record: func(_, l string) string { return l + "/.git/" },
			home: true, want: append(slices.Clone(all), "home"), remembered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.chmod != "" && os.Geteuid() == 0 {
				t.Skip("a read-only directory does not stop root")
			}
			root := realTempDir(t)
			repo := filepath.Join(root, "T")
			registry := filepath.Join(repo, ".git", "worktrees")
			name := tc.entry
			if name == "" {
				name = "w1"
			}
			entry := filepath.Join(registry, name)
			leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
			for _, f := range staleRegFiles {
				writeFile(t, filepath.Join(entry, f), f+"\n", 0o644)
			}
			writeFile(t, filepath.Join(entry, "gitdir"), tc.record(root, leaf), 0o644)
			if err := os.Symlink(repo, filepath.Join(root, "Tlink")); err != nil {
				t.Fatal(err)
			}
			if tc.locked {
				writeFile(t, filepath.Join(entry, "locked"), "", 0o644)
			}
			if tc.home {
				mkdirForTest(t, filepath.Join(entry, "home"))
				t.Setenv("HOME", filepath.Join(entry, "home"))
			}
			switch tc.chmod {
			case "entry":
				chmodForTest(t, entry, 0o500)
			case "registry":
				chmodForTest(t, registry, 0o555)
			}
			t.Cleanup(func() {
				_ = os.Chmod(registry, 0o755)
				_ = os.Chmod(entry, 0o755)
			})
			sentRepo, sentLeaf := repo, leaf
			if tc.viaLink {
				sentRepo = filepath.Join(root, "Tlink")
				sentLeaf = filepath.Join(sentRepo, ".claude", "worktrees", "w1")
			}

			var kept []string
			doneWithin(t, "dropStaleWorktreeRegistration", func() { kept = dropStaleWorktreeRegistration(sentRepo, sentLeaf) })
			_ = os.Chmod(registry, 0o755)
			_ = os.Chmod(entry, 0o755)

			want := slices.Clone(tc.want)
			slices.Sort(want)
			if got := pathsBelow(t, entry); !slices.Equal(got, want) || (got == nil) != (tc.want == nil) {
				t.Errorf("the entry holds %q, want %q (nil means the entry is gone)", got, tc.want)
			}
			var wantKept []string
			if tc.remembered {
				wantKept = []string{filepath.Join(sentRepo, ".git", "worktrees", name)}
			}
			if !slices.Equal(kept, wantKept) {
				t.Errorf("kept = %q, want %q", kept, wantKept)
			}
		})
	}
}

// TestStaleRegistrationRefusal pins which registration gets the text of cells A9 g,
// A9b g and A10 g: only the entry that the step before the add left in place.
func TestStaleRegistrationRefusal(t *testing.T) {
	root := realTempDir(t)
	reg, other := filepath.Join(root, "worktrees", "w1"), filepath.Join(root, "worktrees", "w11")
	mkdirForTest(t, reg)
	mkdirForTest(t, other)
	const leaf = "/x/leaf"
	want := "refusing to create worktree: /x/leaf carries a .git file naming an admin entry other than the one just created for it"
	if got := staleRegistrationRefusal(leaf, reg, []string{reg}); got != want {
		t.Errorf("the entry that stayed: %q, want %q", got, want)
	}
	if got := staleRegistrationRefusal(leaf, reg, []string{other, reg}); got != want {
		t.Errorf("the second of two entries that stayed (not measured): %q, want %q", got, want)
	}
	for _, tc := range []struct {
		name, registration string
		kept               []string
	}{
		{"no entry stayed (row D-12, cell P-c)", reg, nil},
		{"another registration (cells A9 p and A10 p)", other, []string{reg}},
		{"no registration", "", []string{reg}},
	} {
		if got := staleRegistrationRefusal(leaf, tc.registration, tc.kept); got != "" {
			t.Errorf("%s: %q, want no refusal", tc.name, got)
		}
	}
}

// TestWorktreeCreateStaleEntryOfSameName pins cells A9 g and A7 g (89cb6289, Linux
// VM). The state is that of cell P-c: the daemon has GIT_COMMON_DIR of repository
// X, and T holds an old registration w1 with its own index. Here the record of the
// old registration names the .git of the new leaf.
//
//   - A9 g: the record is <L>/.git/ and the entry holds an empty `locked` file. The
//     entry stays. The create is refused with the "other than the one just created"
//     text, the add is the last git call, nothing is rolled back, and the old index
//     keeps its bytes and its inode.
//   - A7 g: the record is the relative path of the leaf. The entry goes before the
//     add, and the create is refused with the "does not name" text. No index is
//     written into the old entry.
func TestWorktreeCreateStaleEntryOfSameName(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record func(leaf string) string
		locked bool
		text   func(leaf string) string
	}{
		{"A9 g", func(l string) string { return l + "/.git/" }, true, func(leaf string) string {
			return "refusing to create worktree: " + leaf + " carries a .git file naming an admin entry other than the one just created for it"
		}},
		{"A7 g", func(string) string { return "../../../.claude/worktrees/w1/.git\n" }, false, notOursText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			T, X, oldReg := stageOldRegistrationOfSameName(t)
			leaf := filepath.Join(T, ".claude", "worktrees", "w1")
			realT, err := filepath.EvalSymlinks(T)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(oldReg, "gitdir"), tc.record(filepath.Join(realT, ".claude", "worktrees", "w1")), 0o644)
			if tc.locked {
				writeFile(t, filepath.Join(oldReg, "locked"), "", 0o644)
			}
			index := filepath.Join(oldReg, "index")
			oldIndex, err := os.ReadFile(index)
			if err != nil || len(oldIndex) == 0 {
				t.Fatalf("the old registration holds no index (err %v): this fixture does not stage the cell", err)
			}
			oldInfo, err := os.Stat(index)
			if err != nil {
				t.Fatal(err)
			}
			regBefore := regFiles(t, oldReg)

			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			installGitSlowStub(t, realGit)
			log := filepath.Join(t.TempDir(), "calls.log")
			t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
			t.Setenv("GIT_COMMON_DIR", filepath.Join(X, ".git"))
			raw := frameWithin(t, "git.worktree_create", createParams(T, leaf, "w1"))
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, tc.text(leaf)) + `,"errorCode":"unsafe_path"}}`
			if raw != want {
				t.Errorf("reply = %s\nwant %s", raw, want)
			}

			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
			if last := lines[len(lines)-1]; !strings.Contains(last, "worktree\x1fadd") {
				t.Errorf("the last git call = %q, want the add", last)
			}
			if tc.locked {
				if b, err := os.ReadFile(index); err != nil || string(b) != string(oldIndex) {
					t.Errorf("the index of the old registration changed (err %v)", err)
				}
				if fi, err := os.Stat(index); err != nil || !os.SameFile(oldInfo, fi) {
					t.Errorf("the index of the old registration is another file now (err %v)", err)
				}
				if got := regFiles(t, oldReg); !slices.Equal(got, regBefore) {
					t.Errorf("the old registration = %q\nwant %q", got, regBefore)
				}
			} else {
				if _, err := os.Lstat(oldReg); !os.IsNotExist(err) {
					t.Errorf("the old registration stays (Lstat err %v), want it removed before the add", err)
				}
				if fi, err := os.Stat(filepath.Dir(oldReg)); err != nil || !fi.IsDir() {
					t.Errorf("the registrations folder of T: %v, want it kept", err)
				}
			}
			ents, err := os.ReadDir(leaf)
			if err != nil || len(ents) != 1 || ents[0].Name() != ".git" {
				t.Errorf("leaf entries = %v (err %v), want only .git", ents, err)
			}
			newReg := filepath.Join(X, ".git", "worktrees", "w1")
			if _, err := os.Stat(filepath.Join(newReg, "HEAD")); err != nil {
				t.Errorf("the registration in X is gone: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(newReg, "index")); !os.IsNotExist(err) {
				t.Errorf("the registration in X holds an index (Lstat err %v), want none", err)
			}
			if _, err := os.Stat(filepath.Join(T, ".git", "refs", "heads", "w1")); err != nil {
				t.Errorf("refs/heads/w1 of T is gone: %v", err)
			}
		})
	}
}

// stubSteps makes the slowed call run steps through the "steps" action of the git
// stub. See runGitSlow.
func stubSteps(t *testing.T, steps ...[]string) {
	t.Helper()
	joined := make([]string, len(steps))
	for i, s := range steps {
		joined[i] = strings.Join(s, "\x1f")
	}
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
	t.Setenv("CLAUSTRUM_GITSTUB_LEAF", "")
	t.Setenv("CLAUSTRUM_GITSTUB_VALUE", strings.Join(joined, "\x1e"))
}

// forcedFailureFrame is the frame of a read-tree that the stub fails with the text
// "fatal: forced read-tree failure".
const forcedFailureFrame = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed (checkout): fatal: forced read-tree failure","errorCode":"worktree_add_failed"}}`

// TestWorktreeCreateRollbackRemovesTestedRegistration pins which registration the
// rollback removes after a read-tree that fails. The stub changes the disk and then
// fails the read-tree. In every cell 89cb6289 answers the plain failed checkout,
// removes the leaf, the registration w1 and branch w1, and keeps every other
// folder. w9 is the registration of a live sibling worktree.
//
//   - Z16 (Linux and macOS VMs): the record of w1 is rewritten to /nonexistent/.git.
//   - B1 (Linux VM): the record of w1 names the .git of the sibling.
//   - Z18 (Linux and macOS VMs): the .git file of the leaf names a folder outside
//     the repository, whose record has mode 000. That folder stays.
//   - B2 (Linux VM): the .git file of the leaf names the registration w9.
//   - B3 (Linux VM): B2, and the record of w9 names the leaf.
//   - B5 (Linux VM): the .git file of the leaf is removed.
func TestWorktreeCreateRollbackRemovesTestedRegistration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sibling bool
		// realLeaf and live have their symlinks resolved, as git records a path.
		steps func(f wtFixture, reg, w9, live, outside, realLeaf string) [][]string
	}{
		{"Z16", false, func(_ wtFixture, reg, _, _, _, _ string) [][]string {
			return [][]string{{"write", filepath.Join(reg, "gitdir"), "/nonexistent/.git\n"}}
		}},
		{"B1", true, func(_ wtFixture, reg, _, live, _, _ string) [][]string {
			return [][]string{{"write", filepath.Join(reg, "gitdir"), filepath.Join(live, ".git") + "\n"}}
		}},
		{"Z18", false, func(f wtFixture, _, _, _, outside, _ string) [][]string {
			return [][]string{{"write", filepath.Join(f.leaf(), ".git"), "gitdir: " + outside + "\n"}}
		}},
		{"B2", true, func(f wtFixture, _, w9, _, _, _ string) [][]string {
			return [][]string{{"write", filepath.Join(f.leaf(), ".git"), "gitdir: " + w9 + "\n"}}
		}},
		{"B3", true, func(f wtFixture, _, w9, _, _, realLeaf string) [][]string {
			return [][]string{
				{"write", filepath.Join(f.leaf(), ".git"), "gitdir: " + w9 + "\n"},
				{"write", filepath.Join(w9, "gitdir"), filepath.Join(realLeaf, ".git") + "\n"},
			}
		}},
		{"B5", false, func(f wtFixture, _, _, _, _, _ string) [][]string {
			return [][]string{{"rm", filepath.Join(f.leaf(), ".git")}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := postAddFixture(t)
			root, err := filepath.EvalSymlinks(filepath.Dir(f.top))
			if err != nil {
				t.Fatal(err)
			}
			realLeaf := filepath.Join(root, "T", ".claude", "worktrees", "w1")
			reg, w9 := filepath.Join(f.regDir, "w1"), filepath.Join(f.regDir, "w9")
			live := filepath.Join(root, "live", "w9")
			outside := filepath.Join(root, "outside", "reg")
			writeFile(t, filepath.Join(outside, "gitdir"), filepath.Join(realLeaf, ".git")+"\n", 0o644)
			chmodForTest(t, filepath.Join(outside, "gitdir"), 0)
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(outside, "gitdir"), 0o644) })
			wantRegs := []string(nil)
			if tc.sibling {
				runGit(t, f.top, "worktree", "add", "-q", "-b", "w9", live)
				wantRegs = []string{"w9"}
			}
			stubSteps(t, tc.steps(f, reg, w9, live, outside, realLeaf)...)
			slowGit(t, "read-tree", "fail", 0, `fatal: forced read-tree failure\n`, "")

			raw, _ := f.create(t, s, "w1", "", 0)
			if raw != forcedFailureFrame {
				t.Errorf("reply = %s\nwant %s", raw, forcedFailureFrame)
			}
			f.assertRolledBack(t, wantRegs, false)
			if _, err := os.Lstat(filepath.Join(outside, "gitdir")); err != nil {
				t.Errorf("the folder outside the repository lost its record: %v", err)
			}
			if !tc.sibling {
				return
			}
			for _, name := range []string{"HEAD", "commondir", "gitdir"} {
				if _, err := os.Stat(filepath.Join(w9, name)); err != nil {
					t.Errorf("the registration of the sibling lost %s: %v", name, err)
				}
			}
			if _, err := os.Stat(filepath.Join(live, "t.txt")); err != nil {
				t.Errorf("the sibling worktree lost its checkout: %v", err)
			}
			if !f.hasRef(t, "w9") {
				t.Error("refs/heads/w9 is gone, want it kept")
			}
		})
	}
}

// TestWorktreeCreateRecordGoneAfterCheckout pins cells B4 (Linux VM) and Z15
// (Linux and macOS VMs) of 89cb6289. The stub runs the real read-tree and then
// removes the gitdir record of the new registration.
//
//   - B4: nothing else changes. The create succeeds, the registration holds the
//     index, and branch w1 stays.
//   - Z15: the registration also gets mode 0500. The frame ends with the openat
//     text and the undo clause of a registration that stays. The leaf goes. Branch
//     w1 stays. In the registration logs/HEAD goes, and HEAD, commondir and logs
//     stay. It needs a user that is not root.
func TestWorktreeCreateRecordGoneAfterCheckout(t *testing.T) {
	t.Run("B4", func(t *testing.T) {
		f, s := postAddFixture(t)
		reg := filepath.Join(f.regDir, "w1")
		stubSteps(t, []string{"rm", filepath.Join(reg, "gitdir")})
		slowGit(t, "read-tree", "post", 0, "", "")
		raw, _ := f.create(t, s, "w1", "", 0)
		b, err := os.ReadFile(filepath.Join(f.leaf(), ".git"))
		if err != nil {
			t.Fatal(err)
		}
		f.wantCreated(t, raw, string(b), reg)
		if _, err := os.Lstat(filepath.Join(reg, "gitdir")); !os.IsNotExist(err) {
			t.Fatalf("the record is there (Lstat err %v): this fixture does not stage the cell", err)
		}
		if !f.hasRef(t, "w1") {
			t.Error("refs/heads/w1 is gone, want it kept")
		}
	})
	t.Run("Z15", func(t *testing.T) {
		f, s, tmp := indexInstallFixture(t)
		reg := filepath.Join(f.regDir, "w1")
		t.Cleanup(func() { _ = os.Chmod(reg, 0o755) })
		stubSteps(t, []string{"rm", filepath.Join(reg, "gitdir")}, []string{"chmod", reg, "500"})
		slowGit(t, "read-tree", "post", 0, `hint: synthetic\n`, "")

		raw, _ := f.create(t, s, "w1", "", 0)
		if err := os.Chmod(reg, 0o755); err != nil {
			t.Fatal(err)
		}
		tail := jsonString(t, " openat w1/index: permission denied; and the undo could not finish for "+f.leaf()+
			": the worktree registration and the branch remain; remove them by hand before retrying (RemoveAll w1: permission denied)")
		tail = tail[1:] + `,"errorCode":"worktree_add_failed"}}`
		if !strings.HasPrefix(raw, checkoutFailedHead) || !strings.HasSuffix(raw, tail) {
			t.Fatalf("reply = %s\nwant %s … %s", raw, checkoutFailedHead, tail)
		}
		if got := f.leafEntries(t); got != nil {
			t.Errorf("leaf entries = %q, want the leaf removed", got)
		}
		// A newer git of the host can make more entries in a registration, so the
		// test names the measured ones only.
		got := pathsBelow(t, reg)
		for _, name := range []string{"HEAD", "commondir", "logs"} {
			if !slices.Contains(got, name) {
				t.Errorf("the registration holds %q, want %s kept", got, name)
			}
		}
		for _, name := range []string{"logs/HEAD", "gitdir", "index"} {
			if slices.Contains(got, name) {
				t.Errorf("the registration holds %q, want no %s", got, name)
			}
		}
		if !f.hasRef(t, "w1") {
			t.Error("refs/heads/w1 was deleted, want it kept beside a registration that stays")
		}
		requireNoIndexTemp(t, tmp)
	})
}

// TestWorktreeCreateRollbackKeepsReplacedRegistration pins claustrum's own identity
// guard of the rollback (D24). The stub changes the registrations folder and then
// fails the read-tree. claustrum removes no folder of it. The frame is the plain
// failed checkout, and the leaf and branch w1 go, as on 89cb6289.
//
//   - B6 (Linux VM): the hook renames the new registration w1 to w1x and makes a
//     new empty folder w1. 89cb6289 removes the empty w1 and keeps w1x.
//   - B6b (Linux VM): the hook renames w1 to w1x, and the registration w9 of a live
//     sibling worktree to w1. 89cb6289 removes that w1 with the files of the
//     sibling, and keeps w1x.
func TestWorktreeCreateRollbackKeepsReplacedRegistration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sibling bool
	}{{"B6", false}, {"B6b", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := postAddFixture(t)
			reg, moved, w9 := filepath.Join(f.regDir, "w1"), filepath.Join(f.regDir, "w1x"), filepath.Join(f.regDir, "w9")
			live := filepath.Join(filepath.Dir(f.top), "live", "w9")
			steps := [][]string{{"rename", reg, moved}, {"mkdir", reg}}
			if tc.sibling {
				runGit(t, f.top, "worktree", "add", "-q", "-b", "w9", live)
				steps[1] = []string{"rename", w9, reg}
			}
			stubSteps(t, steps...)
			slowGit(t, "read-tree", "fail", 0, `fatal: forced read-tree failure\n`, "")

			raw, _ := f.create(t, s, "w1", "", 0)
			if raw != forcedFailureFrame {
				t.Errorf("reply = %s\nwant %s", raw, forcedFailureFrame)
			}
			f.assertRolledBack(t, []string{"w1", "w1x"}, false)
			if b, err := os.ReadFile(filepath.Join(moved, "gitdir")); err != nil || !strings.HasSuffix(strings.TrimSpace(string(b)), filepath.Join("worktrees", "w1", ".git")) {
				t.Errorf("the record of w1x = %q (err %v), want that of the new worktree", b, err)
			}
			if !tc.sibling {
				if got := pathsBelow(t, reg); got == nil || len(got) != 0 {
					t.Errorf("w1 holds %q, want the empty folder kept", got)
				}
				return
			}
			if b, err := os.ReadFile(filepath.Join(reg, "gitdir")); err != nil || !strings.HasSuffix(strings.TrimSpace(string(b)), filepath.Join("live", "w9", ".git")) {
				t.Errorf("the record of w1 = %q (err %v), want that of the sibling", b, err)
			}
			if _, err := os.Stat(filepath.Join(reg, "HEAD")); err != nil {
				t.Errorf("the files of the sibling are gone: %v", err)
			}
			if _, err := os.Stat(filepath.Join(live, "t.txt")); err != nil {
				t.Errorf("the sibling worktree lost its checkout: %v", err)
			}
			if !f.hasRef(t, "w9") {
				t.Error("refs/heads/w9 is gone, want it kept")
			}
		})
	}
}

// TestTestedRegistrationGuards pins the guards of a tested registration at the
// function level. No cell measured these states.
//
//   - The index goes into the tested registration with no gitdir record (the rule
//     of cell B4).
//   - A folder that replaced the registration gets no index, and the rollback
//     delete leaves it (D24).
//   - A registration that holds HOME is not deleted (D2).
//   - The control: the tested registration itself goes.
func TestTestedRegistrationGuards(t *testing.T) {
	root := realTempDir(t)
	registry := filepath.Join(root, ".git", "worktrees")
	reg := filepath.Join(registry, "w1")
	writeFile(t, filepath.Join(reg, "HEAD"), "ref: refs/heads/w1\n", 0o644)
	src := filepath.Join(root, "new-index")
	writeFile(t, src, "new\n", 0o644)
	tested := acceptRegistration(reg)
	defer tested.release()
	if tested.path != reg || tested.info == nil {
		t.Fatalf("acceptRegistration = %+v, want the registration with its identity", tested)
	}
	if got := acceptRegistration(""); got.path != "" {
		t.Errorf("acceptRegistration of no registration = %+v, want the zero value", got)
	}

	if err := guardedInstallWorktreeIndex(src, reg, filepath.Join(root, "leaf"), tested); err != nil {
		t.Fatalf("placement with no record: %v, want the index placed", err)
	}
	wantFileContent(t, filepath.Join(reg, "index"), "new\n")

	// Another folder takes the place of the registration.
	if err := os.Rename(reg, filepath.Join(registry, "w1x")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(reg, "HEAD"), "ref: refs/heads/other\n", 0o644)
	err := guardedInstallWorktreeIndex(src, reg, filepath.Join(root, "leaf"), tested)
	if want := "the registration " + reg + " is not the folder that was tested after the add"; err == nil || err.Error() != want {
		t.Errorf("placement into a replaced folder = %v, want %s", err, want)
	}
	if _, err := os.Lstat(filepath.Join(reg, "index")); !os.IsNotExist(err) {
		t.Errorf("the replaced folder holds an index (Lstat err %v), want none", err)
	}
	if err := removeTestedRegistration(tested); err != nil {
		t.Errorf("removeTestedRegistration of a replaced folder = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(reg, "HEAD")); err != nil {
		t.Errorf("the replaced folder was deleted: %v", err)
	}

	// HOME in the registration.
	home := filepath.Join(reg, "home")
	mkdirForTest(t, home)
	t.Setenv("HOME", home)
	second := acceptRegistration(reg)
	defer second.release()
	if err := removeTestedRegistration(second); err != nil {
		t.Errorf("removeTestedRegistration of a folder that holds HOME = %v, want nil", err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Errorf("the folder that holds HOME was deleted: %v", err)
	}

	// The control.
	t.Setenv("HOME", filepath.Join(root, "elsewhere"))
	if err := removeTestedRegistration(second); err != nil {
		t.Errorf("removeTestedRegistration: %v", err)
	}
	if _, err := os.Lstat(reg); !os.IsNotExist(err) {
		t.Errorf("the tested registration stays (Lstat err %v), want it deleted", err)
	}
}

// TestTestedRegistrationRetargetedRegistry pins that the identity guard (D24) tests
// the folder that the delete and the placement then act through. The registrations
// folder is a symlink. Right before each open of it, the test points the link at
// another folder that holds an entry of the same name. Nothing in that other
// folder is deleted, and no index goes into it. No cell measured this state.
func TestTestedRegistrationRetargetedRegistry(t *testing.T) {
	for _, op := range []string{"delete", "placement"} {
		t.Run(op, func(t *testing.T) {
			root := realTempDir(t)
			link := filepath.Join(root, "worktrees")
			writeFile(t, filepath.Join(root, "a", "w1", "HEAD"), "accepted\n", 0o644)
			writeFile(t, filepath.Join(root, "b", "w1", "HEAD"), "other\n", 0o644)
			if err := os.Symlink(filepath.Join(root, "a"), link); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(root, "new-index")
			writeFile(t, src, "new\n", 0o644)
			tested := acceptRegistration(filepath.Join(link, "w1"))
			defer tested.release()

			old := openRegistrationsRoot
			t.Cleanup(func() { openRegistrationsRoot = old })
			opens := 0
			openRegistrationsRoot = func(name string) (*os.Root, error) {
				opens++
				if err := os.Remove(link); err != nil {
					return nil, err
				}
				if err := os.Symlink(filepath.Join(root, "b"), link); err != nil {
					return nil, err
				}
				return old(name)
			}
			var err error
			if op == "delete" {
				err = removeTestedRegistration(tested)
				if err != nil {
					t.Errorf("removeTestedRegistration = %v, want nil", err)
				}
			} else {
				err = guardedInstallWorktreeIndex(src, tested.path, filepath.Join(root, "leaf"), tested)
				if want := "the registration " + tested.path + " is not the folder that was tested after the add"; err == nil || err.Error() != want {
					t.Errorf("placement = %v, want %s", err, want)
				}
			}
			if opens != 1 {
				t.Fatalf("the registrations folder was opened %d times, want 1: the test did not stage the change", opens)
			}
			if got, want := pathsBelow(t, filepath.Join(root, "b", "w1")), []string{"HEAD"}; !slices.Equal(got, want) {
				t.Errorf("the entry of the other folder holds %q, want %q", got, want)
			}
			wantFileContent(t, filepath.Join(root, "a", "w1", "HEAD"), "accepted\n")
		})
	}
}

// TestTestedRegistrationHeldIdentity pins that a folder which is deleted and made
// again at the path of the accepted registration does not pass for it. The
// accepted folder is held open, so the new one cannot get its number.
func TestTestedRegistrationHeldIdentity(t *testing.T) {
	root := realTempDir(t)
	reg := filepath.Join(root, "worktrees", "w1")
	for range 20 {
		mkdirForTest(t, reg)
		tested := acceptRegistration(reg)
		if tested.held == nil {
			t.Fatal("acceptRegistration holds no handle on the registration")
		}
		if err := os.Remove(reg); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(reg, "HEAD"), "another\n", 0o644)
		err := removeTestedRegistration(tested)
		tested.release()
		if err != nil {
			t.Fatalf("removeTestedRegistration = %v, want nil", err)
		}
		if _, err := os.Stat(filepath.Join(reg, "HEAD")); err != nil {
			t.Fatalf("the new folder was deleted: %v", err)
		}
		if err := os.RemoveAll(reg); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDropStaleWorktreeRegistrationEveryEntry pins the step before the add with two
// stale entries. No cell of 89cb6289 measured that state. claustrum handles each
// entry: both go, and with a `locked` file in one, that one stays and is
// remembered.
func TestDropStaleWorktreeRegistrationEveryEntry(t *testing.T) {
	for _, locked := range []bool{false, true} {
		root := realTempDir(t)
		repo := filepath.Join(root, "T")
		registry := filepath.Join(repo, ".git", "worktrees")
		leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
		for _, name := range []string{"old9", "w1"} {
			writeFile(t, filepath.Join(registry, name, "gitdir"), leaf+"/.git\n", 0o644)
		}
		var want, wantKept []string
		if locked {
			writeFile(t, filepath.Join(registry, "w1", "locked"), "", 0o644)
			want, wantKept = []string{"w1"}, []string{filepath.Join(registry, "w1")}
		}
		kept := dropStaleWorktreeRegistration(repo, leaf)
		ents, err := os.ReadDir(registry)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range ents {
			got = append(got, e.Name())
		}
		if !slices.Equal(got, want) {
			t.Errorf("locked %v: the registrations folder holds %q, want %q", locked, got, want)
		}
		if !slices.Equal(kept, wantKept) {
			t.Errorf("locked %v: kept = %q, want %q", locked, kept, wantKept)
		}
	}
}

// TestWorktreeCreateSecondStaleEntry pins a create with two stale entries in
// baseRepo. No cell of 89cb6289 measured that state. The state is that of cell
// A9b g, and T also holds a stale entry old9 that can be removed. The directory
// read gives old9 first. claustrum removes old9, keeps the locked w1 and refuses
// the create with the text of cell A9b g. The index of w1 keeps its bytes.
func TestWorktreeCreateSecondStaleEntry(t *testing.T) {
	T, X, oldReg := stageOldRegistrationOfSameName(t)
	leaf := filepath.Join(T, ".claude", "worktrees", "w1")
	realT, err := filepath.EvalSymlinks(T)
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(realT, ".claude", "worktrees", "w1", ".git") + "\n"
	writeFile(t, filepath.Join(oldReg, "gitdir"), record, 0o644)
	writeFile(t, filepath.Join(oldReg, "locked"), "", 0o644)
	old9 := filepath.Join(filepath.Dir(oldReg), "old9")
	writeFile(t, filepath.Join(old9, "gitdir"), record, 0o644)
	index := filepath.Join(oldReg, "index")
	oldIndex, err := os.ReadFile(index)
	if err != nil || len(oldIndex) == 0 {
		t.Fatalf("the old registration holds no index (err %v): this fixture does not stage the state", err)
	}

	t.Setenv("GIT_COMMON_DIR", filepath.Join(X, ".git"))
	raw := frameWithin(t, "git.worktree_create", createParams(T, leaf, "w1"))
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, "refusing to create worktree: "+leaf+
		" carries a .git file naming an admin entry other than the one just created for it") + `,"errorCode":"unsafe_path"}}`
	if raw != want {
		t.Errorf("reply = %s\nwant %s", raw, want)
	}
	if _, err := os.Lstat(old9); !os.IsNotExist(err) {
		t.Errorf("old9 stays (Lstat err %v), want it removed", err)
	}
	if b, err := os.ReadFile(index); err != nil || string(b) != string(oldIndex) {
		t.Errorf("the index of the locked entry changed (err %v)", err)
	}
}

// TestDropStaleWorktreeRegistrationRetargetedRegistry pins that the step before the
// add lists, reads and removes in one opened registrations folder. That folder is
// a symlink, and the test points the link at another folder around the open. One
// entry w1 has a record that names the leaf, and the other names another worktree.
// No cell measured this state.
//
//   - The link changes right before the open, to the folder with the other record.
//     Nothing goes in either folder.
//   - The link changes right after the open, to the folder with the stale record.
//     The opened folder holds the other record, so nothing goes in either folder.
func TestDropStaleWorktreeRegistrationRetargetedRegistry(t *testing.T) {
	for _, when := range []string{"before the open", "after the open"} {
		t.Run(when, func(t *testing.T) {
			root := realTempDir(t)
			repo := filepath.Join(root, "T")
			leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
			link := filepath.Join(repo, ".git", "worktrees")
			stale, other := filepath.Join(root, "stale"), filepath.Join(root, "other")
			writeFile(t, filepath.Join(stale, "w1", "gitdir"), leaf+"/.git\n", 0o644)
			writeFile(t, filepath.Join(other, "w1", "gitdir"), filepath.Join(root, "live", "w1", ".git")+"\n", 0o644)
			first, second := stale, other
			if when == "after the open" {
				first, second = other, stale
			}
			mkdirForTest(t, filepath.Dir(link))
			if err := os.Symlink(first, link); err != nil {
				t.Fatal(err)
			}
			retarget := func() error {
				if err := os.Remove(link); err != nil {
					return err
				}
				return os.Symlink(second, link)
			}
			old := openRegistrationsRoot
			t.Cleanup(func() { openRegistrationsRoot = old })
			opens := 0
			openRegistrationsRoot = func(name string) (*os.Root, error) {
				opens++
				if when == "before the open" {
					if err := retarget(); err != nil {
						return nil, err
					}
					return old(name)
				}
				r, err := old(name)
				if err == nil {
					err = retarget()
				}
				return r, err
			}
			kept := dropStaleWorktreeRegistration(repo, leaf)
			if opens != 1 {
				t.Fatalf("the registrations folder was opened %d times, want 1: the test did not stage the change", opens)
			}
			if len(kept) != 0 {
				t.Errorf("kept = %q, want none", kept)
			}
			for _, dir := range []string{stale, other} {
				if got, want := pathsBelow(t, dir), []string{"w1", "w1/gitdir"}; !slices.Equal(got, want) {
					t.Errorf("%s holds %q, want %q", filepath.Base(dir), got, want)
				}
			}
		})
	}
}
