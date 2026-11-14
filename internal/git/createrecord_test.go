package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/sessiondirs"
)

// A good git.worktree_create records the git directory of its repository as a
// session folder, and the record stays after git.worktree_remove. git.info,
// git.status and git.list_branches record nothing.
func TestCreateRecordsTheRepositoryGitDir(t *testing.T) {
	r := newTrustRepo(t)
	dir := filepath.Join(t.TempDir(), sessiondirs.RecordFolderName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessiondirs.UseForTest(dir))
	records := func() []string {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}
	info(t, r.T)
	status(t, r.T, r.T)
	listBranches(t, r.T)
	if got := records(); len(got) != 0 {
		t.Fatalf("the read-only calls wrote records %v", got)
	}
	c, wt := create(t, r.T)
	wantResultPrefix(t, "create", c, `{"success":true,"path":"`+jsonEscape(t, wt)+`"`)
	got := records()
	if len(got) != 1 {
		t.Fatalf("after the create the record folder holds %v, want one record", got)
	}
	b, err := os.ReadFile(filepath.Join(dir, got[0]))
	if err != nil {
		t.Fatal(err)
	}
	_, path, _ := strings.Cut(string(b), "\n")
	if want := filepath.Join(r.T, ".git"); canonicalPath(path) != canonicalPath(want) || got[0] != sessiondirs.RecordName(path) {
		t.Errorf("the record %s holds the path %q, want %q", got[0], path, want)
	}
	wantResultPrefix(t, "remove", remove(t, r.T, wt, "w1"), `{"success":true`)
	if after := records(); len(after) != 1 || after[0] != got[0] {
		t.Errorf("after the remove the record folder holds %v, want %v", after, got)
	}
	// A create from the linked worktree TW records the git directory of T. The
	// record of T goes first, and a fresh store does not know it.
	if err := os.Remove(filepath.Join(dir, got[0])); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessiondirs.UseForTest(dir))
	tw := filepath.Join(r.TW, ".claude", "worktrees", "w2")
	wantResultPrefix(t, "create from TW", trustCall(t, "git.worktree_create", map[string]any{
		"baseRepo": r.TW, "worktreePath": tw, "branchName": "w2"}), `{"success":true`)
	if after := records(); len(after) != 1 || after[0] != got[0] {
		t.Errorf("after the create from TW the record folder holds %v, want %v", after, got)
	}
}

// A create with the off switch writes no record.
func TestCreateWithTheOffSwitchRecordsNothing(t *testing.T) {
	r := newTrustRepo(t)
	dir := filepath.Join(t.TempDir(), sessiondirs.RecordFolderName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessiondirs.UseForTest(dir))
	wt := filepath.Join(r.T, ".claude", "worktrees", "w1")
	wantResultPrefix(t, "create", trustCall(t, "git.worktree_create", map[string]any{
		"baseRepo": r.T, "worktreePath": wt, "branchName": "w1", "sessionFoldersOff": true}), `{"success":true`)
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("a create with the off switch: record folder %v %v, want empty", entries, err)
	}
}

// A create whose `git worktree add` fails keeps the record of its git
// directory, because the record is written before the add. c9125eb8 and
// 4c111e86 keep it so (measured on Linux and macOS). The git stub fails the add.
func TestCreateRecordsBeforeTheAdd(t *testing.T) {
	f := newCheckoutRuleFixture(t)
	dir := filepath.Join(t.TempDir(), sessiondirs.RecordFolderName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessiondirs.UseForTest(dir))
	slowGit(t, "worktree,add", "fail", 0, "fatal: the stub fails the add\n", "")
	raw, _ := f.create(t, nil)
	if !strings.Contains(raw, `"success":false`) || !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
		t.Fatalf("git.worktree_create = %s, want a failed add", raw)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("after the failed add the record folder holds %v %v, want one record", entries, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if _, path, _ := strings.Cut(string(b), "\n"); canonicalPath(path) != canonicalPath(f.gitDir) {
		t.Errorf("the record holds the path %q, want %q", path, f.gitDir)
	}
}
