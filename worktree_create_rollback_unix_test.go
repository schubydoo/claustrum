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
// those of 89cb6289 on Linux and macOS VMs, 2 runs each. Cells Z15, Z16 and
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
		t.Errorf("the second of two entries that stayed (cell A13 g): %q, want %q", got, want)
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
// and macOS VMs). The state is that of cell P-c: the daemon has GIT_COMMON_DIR of repository
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
			return "refusing to create worktree: " + namedLeaf(leaf) + " carries a .git file naming an admin entry other than the one just created for it"
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
//   - B1 (Linux and macOS VMs): the record of w1 names the .git of the sibling.
//   - Z18 (Linux and macOS VMs): the .git file of the leaf names a folder outside
//     the repository, whose record has mode 000. That folder stays.
//   - B2 (Linux and macOS VMs): the .git file of the leaf names the registration w9.
//   - B3 (Linux and macOS VMs): B2, and the record of w9 names the leaf.
//   - B5 (Linux and macOS VMs): the .git file of the leaf is removed.
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

// TestWorktreeCreateRecordGoneAfterCheckout pins cells B4 and Z15
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
//   - B6 (Linux and macOS VMs): the hook renames the new registration w1 to w1x and makes a
//     new empty folder w1. 89cb6289 removes the empty w1 and keeps w1x.
//   - B6b (Linux and macOS VMs): the hook renames w1 to w1x, and the registration w9 of a live
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

// TestDropStaleWorktreeRegistrationCount pins how many stale entries the step
// before the add removes: one, and only if it is the only stale entry of the
// folder. Cells A13 and A13b are those of 89cb6289 on Linux and macOS VMs. The S
// cells are those of 89cb6289 on a Linux VM. "stale" is the record <L>/.git/ of
// cell A1.
//
//   - S1 (old8, old9 and w1 stale), S2 (old8 and old9 stale), A13 (old9 and w1
//     stale) and S6 (two stale records in two spellings): every entry stays.
//   - S5 and A13b: one of two stale entries holds a `locked` file. Both stay.
//   - S3 and S4: one entry is stale, and the other has the record of another
//     worktree. The stale one goes.
//   - S7, S7b and S7c: beside the stale old9, w1 is a regular file, a folder with
//     no gitdir record, or an empty folder. old9 goes.
func TestDropStaleWorktreeRegistrationCount(t *testing.T) {
	type entries map[string]string
	for _, tc := range []struct {
		name string
		in   entries
		want []string // the names that stay
		kept []string // the names that are remembered
	}{
		{"S1", entries{"old8": "stale", "old9": "stale", "w1": "stale"}, []string{"old8", "old9", "w1"}, []string{"old8", "old9", "w1"}},
		{"S2", entries{"old8": "stale", "old9": "stale"}, []string{"old8", "old9"}, []string{"old8", "old9"}},
		{"S3", entries{"old9": "stale", "w1": "other"}, []string{"w1"}, nil},
		{"S4", entries{"w1": "stale", "old9": "other"}, []string{"old9"}, nil},
		{"S5", entries{"old9": "locked", "w1": "stale"}, []string{"old9", "w1"}, []string{"old9", "w1"}},
		{"S6", entries{"old9": "stale newline", "w1": "stale"}, []string{"old9", "w1"}, []string{"old9", "w1"}},
		{"S7", entries{"old9": "stale", "w1": "file"}, []string{"w1"}, nil},
		{"S7b", entries{"old9": "stale", "w1": "no record"}, []string{"w1"}, nil},
		{"S7c", entries{"old9": "stale", "w1": "empty"}, []string{"w1"}, nil},
		{"A13", entries{"old9": "stale", "w1": "stale"}, []string{"old9", "w1"}, []string{"old9", "w1"}},
		{"A13b", entries{"old9": "stale", "w1": "locked"}, []string{"old9", "w1"}, []string{"old9", "w1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := realTempDir(t)
			repo := filepath.Join(root, "T")
			registry := filepath.Join(repo, ".git", "worktrees")
			leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
			for name, kind := range tc.in {
				entry := filepath.Join(registry, name)
				switch kind {
				case "stale", "locked":
					writeFile(t, filepath.Join(entry, "gitdir"), leaf+"/.git/", 0o644)
					if kind == "locked" {
						writeFile(t, filepath.Join(entry, "locked"), "", 0o644)
					}
				case "stale newline":
					writeFile(t, filepath.Join(entry, "gitdir"), leaf+"/.git\n", 0o644)
				case "other":
					writeFile(t, filepath.Join(entry, "gitdir"), filepath.Join(root, "gone", "w1", ".git")+"\n", 0o644)
				case "file":
					writeFile(t, entry, "a file\n", 0o644)
				case "no record":
					writeFile(t, filepath.Join(entry, "HEAD"), "ref: refs/heads/x\n", 0o644)
				case "empty":
					mkdirForTest(t, entry)
				}
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
			if !slices.Equal(got, tc.want) {
				t.Errorf("the registrations folder holds %q, want %q", got, tc.want)
			}
			var wantKept []string
			for _, name := range tc.kept {
				wantKept = append(wantKept, filepath.Join(registry, name))
			}
			if !slices.Equal(kept, wantKept) {
				t.Errorf("kept = %q, want %q", kept, wantKept)
			}
		})
	}
}

// TestWorktreeCreateStaleEntryCount pins a create with more than one old entry in
// baseRepo, with the daemon GIT_COMMON_DIR of repository X (the g cells). The
// state is that of cell P-c, and w1 holds its own index. Cell A13 is that of
// 89cb6289 on Linux and macOS VMs, and the S cells on a Linux VM.
//
//   - S1 g: old8, old9 and w1 are stale. All stay, and the create is refused with
//     the "other than the one just created" text.
//   - A13 g: old9 and w1 are stale. Both stay, and the answer is that text.
//   - S5 g: old9 is stale and locked, and w1 is stale. Both stay, the same text.
//   - S3 g: old9 is stale, and w1 has the record of another worktree. old9 goes,
//     and the answer is the "different worktree" text.
//
// In every cell the index of w1 keeps its bytes.
func TestWorktreeCreateStaleEntryCount(t *testing.T) {
	textC := func(leaf string) string {
		return "refusing to create worktree: " + namedLeaf(leaf) + " carries a .git file naming an admin entry other than the one just created for it"
	}
	for _, tc := range []struct {
		name    string
		others  []string // hand-made stale entries beside w1
		locked  string
		w1Stale bool
		stay    []string
		text    func(leaf string) string
	}{
		{"S1 g", []string{"old8", "old9"}, "", true, []string{"old8", "old9", "w1"}, textC},
		{"A13 g", []string{"old9"}, "", true, []string{"old9", "w1"}, textC},
		{"S5 g", []string{"old9"}, "old9", true, []string{"old9", "w1"}, textC},
		{"S3 g", []string{"old9"}, "", false, []string{"w1"}, differentWorktreeText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			T, X, oldReg := stageOldRegistrationOfSameName(t)
			leaf := filepath.Join(T, ".claude", "worktrees", "w1")
			realT, err := filepath.EvalSymlinks(T)
			if err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(realT, ".claude", "worktrees", "w1", ".git") + "/"
			registry := filepath.Dir(oldReg)
			if tc.w1Stale {
				writeFile(t, filepath.Join(oldReg, "gitdir"), record, 0o644)
			}
			for _, name := range tc.others {
				writeFile(t, filepath.Join(registry, name, "gitdir"), record, 0o644)
			}
			if tc.locked != "" {
				writeFile(t, filepath.Join(registry, tc.locked, "locked"), "", 0o644)
			}
			index := filepath.Join(oldReg, "index")
			oldIndex, err := os.ReadFile(index)
			if err != nil || len(oldIndex) == 0 {
				t.Fatalf("the old registration holds no index (err %v): this fixture does not stage the cell", err)
			}

			t.Setenv("GIT_COMMON_DIR", filepath.Join(X, ".git"))
			raw := frameWithin(t, "git.worktree_create", createParams(T, leaf, "w1"))
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, tc.text(leaf)) + `,"errorCode":"unsafe_path"}}`
			if raw != want {
				t.Errorf("reply = %s\nwant %s", raw, want)
			}
			ents, err := os.ReadDir(registry)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, e := range ents {
				got = append(got, e.Name())
			}
			if !slices.Equal(got, tc.stay) {
				t.Errorf("the registrations folder of T holds %q, want %q", got, tc.stay)
			}
			if b, err := os.ReadFile(index); err != nil || string(b) != string(oldIndex) {
				t.Errorf("the index of w1 changed (err %v)", err)
			}
		})
	}
}

// TestWorktreeCreateRefusalNamesResolvedLeaf pins the spelling of the leaf in five
// refusals. baseRepo is behind a symlink, <F>/Tlink for <F>/T, and both request
// paths go through the link. 89cb6289 names the leaf with the link resolved,
// <F>/T/.claude/worktrees/w1. Cell A12c g ran on Linux and macOS VMs, the T cells
// on a Linux VM.
//
//   - T1 and A12c g: the old entry w1 of T is stale and goes. The "does not name"
//     text.
//   - T2: the state of cell P-c. The "different worktree" text.
//   - T3: the old entry is stale and locked. The "other than the one just
//     created" text.
//   - T4: T has no worktrees folder. The "was not populated" text.
//   - T9: the leaf exists before the request. The "already exists" text.
func TestWorktreeCreateRefusalNamesResolvedLeaf(t *testing.T) {
	for _, tc := range []struct {
		name, stage, text string
	}{
		{"T1", "stale", " carries a .git file that does not name this repository's own worktree admin directory"},
		{"T2", "", " carries a .git file naming an admin directory whose own record is of a different worktree"},
		{"T3", "stale locked", " carries a .git file naming an admin entry other than the one just created for it"},
		{"T4", "no folder", " was not populated by git worktree add"},
		{"T9", "leaf exists", " already exists, and a new worktree is only ever created in a fresh directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			T, X, oldReg := stageOldRegistrationOfSameName(t)
			realT, err := filepath.EvalSymlinks(T)
			if err != nil {
				t.Fatal(err)
			}
			realLeaf := filepath.Join(realT, ".claude", "worktrees", "w1")
			link := filepath.Join(filepath.Dir(T), "Tlink")
			if err := os.Symlink(T, link); err != nil {
				t.Fatal(err)
			}
			sentLeaf := filepath.Join(link, ".claude", "worktrees", "w1")
			switch tc.stage {
			case "stale", "stale locked":
				writeFile(t, filepath.Join(oldReg, "gitdir"), realLeaf+"/.git/", 0o644)
				if tc.stage == "stale locked" {
					writeFile(t, filepath.Join(oldReg, "locked"), "", 0o644)
				}
			case "no folder":
				if err := os.RemoveAll(filepath.Dir(oldReg)); err != nil {
					t.Fatal(err)
				}
			case "leaf exists":
				mkdirForTest(t, realLeaf)
			}
			if tc.stage != "leaf exists" {
				t.Setenv("GIT_COMMON_DIR", filepath.Join(X, ".git"))
			}
			raw := frameWithin(t, "git.worktree_create", createParams(link, sentLeaf, "w1"))
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
				jsonString(t, "refusing to create worktree: "+realLeaf+tc.text) + `,"errorCode":"unsafe_path"}}`
			if raw != want {
				t.Errorf("reply = %s\nwant %s", raw, want)
			}
		})
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

// TestWorktreeCreateAlreadyExistsSpelling pins the path in the "already exists"
// refusal without worktreeRoot (89cb6289, Linux VM). The parent folder of the leaf
// has its symlinks resolved, the last name stays, and the path is cleaned. "L" is
// a request through <F>/Tlink, a symlink to <F>/T. "R" is a request with the real
// path.
//
//   - U1a L and R: the leaf is a symlink to a folder. The text names the leaf,
//     not the target of the link.
//   - U1b L: the leaf is a dangling symlink.
//   - U1d R: the leaf is a symlink to a regular file.
//   - U2a: the leaf is a folder, and the path ends with a slash.
func TestWorktreeCreateAlreadyExistsSpelling(t *testing.T) {
	for _, tc := range []struct {
		name, leafKind string
		viaLink        bool
		suffix         string
	}{
		{"U1a L", "link to a folder", true, ""},
		{"U1a R", "link to a folder", false, ""},
		{"U1b L", "dangling link", true, ""},
		{"U1d R", "link to a file", false, ""},
		{"U2a", "folder", false, "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			T, _, _ := stageOldRegistrationOfSameName(t)
			realT, err := filepath.EvalSymlinks(T)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(realT)
			realLeaf := filepath.Join(realT, ".claude", "worktrees", "w1")
			mkdirForTest(t, filepath.Dir(realLeaf))
			target := filepath.Join(root, "elsewhere", "d")
			switch tc.leafKind {
			case "link to a folder":
				mkdirForTest(t, target)
			case "link to a file":
				writeFile(t, target, "a file\n", 0o644)
			}
			if tc.leafKind == "folder" {
				writeFile(t, filepath.Join(realLeaf, "keep.txt"), "keep\n", 0o644)
			} else if err := os.Symlink(target, realLeaf); err != nil {
				t.Fatal(err)
			}
			base := realT
			if tc.viaLink {
				base = filepath.Join(root, "Tlink")
				if err := os.Symlink(realT, base); err != nil {
					t.Fatal(err)
				}
			}
			sent := base + "/.claude/worktrees/w1" + tc.suffix
			raw := frameWithin(t, "git.worktree_create", createParams(base, sent, "w1"))
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, "refusing to create worktree: "+realLeaf+
				" already exists, and a new worktree is only ever created in a fresh directory") + `,"errorCode":"unsafe_path"}}`
			if raw != want {
				t.Errorf("reply = %s\nwant %s", raw, want)
			}
			if fi, err := os.Lstat(realLeaf); err != nil || (tc.leafKind != "folder" && fi.Mode()&os.ModeSymlink == 0) {
				t.Errorf("the leaf changed (err %v)", err)
			}
		})
	}
}

// TestWorktreeCreateRootBehindSymlinkSpelling pins the path in two refusals with a
// worktreeRoot behind a symlink (89cb6289, Linux VM). worktreeRoot is <F>/Rlink, a
// symlink to <F>/pr/R, and worktreePath goes through the link. baseRepo is real.
//
//   - U3a: <F>/pr/R/cp holds the folder w1 and no marker. The "is not marked as a
//     worktree directory" text names <F>/pr/R/cp.
//   - U3a2: a first create of w0 marks <F>/pr/R/cp. The folder w1 then exists
//     before its create. The "already exists" text names <F>/pr/R/cp/w1.
func TestWorktreeCreateRootBehindSymlinkSpelling(t *testing.T) {
	stage := func(t *testing.T) (repo, realCP, link string) {
		t.Helper()
		requireGit(t)
		base := realTempDir(t)
		requireTempOutsideCheckout(t, base)
		repo = filepath.Join(base, "T")
		mkdirForTest(t, repo)
		runGit(t, repo, "init", "-q")
		runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
		realRoot := filepath.Join(base, "pr", "R")
		mkdirForTest(t, realRoot)
		link = filepath.Join(base, "Rlink")
		if err := os.Symlink(realRoot, link); err != nil {
			t.Fatal(err)
		}
		return repo, filepath.Join(realRoot, "cp"), link
	}
	create := func(t *testing.T, repo, link, name string) string {
		t.Helper()
		return dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create", map[string]any{
			"baseRepo": repo, "branchName": name, "worktreePath": filepath.Join(link, "cp", name), "worktreeRoot": link}))
	}
	t.Run("U3a", func(t *testing.T) {
		repo, realCP, link := stage(t)
		writeFile(t, filepath.Join(realCP, "w1", "keep.txt"), "keep\n", 0o644)
		wantError(t, create(t, repo, link, "w1"), "refusing to create worktree: "+realCP+" already exists, is not marked as a "+
			"worktree directory, and holds other files (for example \"w1\"); the per-repository "+
			"directory under a worktree location must start out empty — remove it, restore "+
			"its .claude-managed-worktrees file if you deleted it, or choose another location", "unsafe_path")
	})
	t.Run("U3a2", func(t *testing.T) {
		repo, realCP, link := stage(t)
		if raw := create(t, repo, link, "w0"); !strings.Contains(raw, `"success":true`) {
			t.Fatalf("the first create = %s, want success: this fixture does not stage the cell", raw)
		}
		writeFile(t, filepath.Join(realCP, "w1", "keep.txt"), "keep\n", 0o644)
		wantError(t, create(t, repo, link, "w1"), "refusing to create worktree: "+filepath.Join(realCP, "w1")+
			" already exists, and a new worktree is only ever created in a fresh directory", "unsafe_path")
	})
}
