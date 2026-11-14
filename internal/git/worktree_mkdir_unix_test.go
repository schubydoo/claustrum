//go:build unix

package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 7d193f89 creates the worktree directory itself before `git worktree add`, so
// an unwritable parent fails as
// "failed to create worktree directory: mkdirat <leaf>: permission denied" with
// errorCode mkdir_failed — NOT git's worktree_add_failed, and NOT os.Mkdir's
// "mkdir <full path>". The `mkdirat <leaf>` wording is the wire-visible bit this
// pins. Unix + non-root (relies on a 0500 parent to deny the leaf mkdir).
func TestWorktreeCreateMkdirLeafFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this case relies on")
	}
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	holder := filepath.Join(repo, ".claude", "worktrees")
	if err := os.MkdirAll(holder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(holder, 0o500); err != nil { // leaf mkdir denied
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(holder, 0o700) })

	h := newTestGitDeps()
	raw := gitDispatch(t, h, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "x", "worktreePath": filepath.Join(holder, "wt")}))
	if !strings.Contains(raw, `"errorCode":"mkdir_failed"`) ||
		!strings.Contains(raw, "failed to create worktree directory: mkdirat wt:") {
		t.Errorf("create with unwritable parent = %s, want mkdir_failed with 'mkdirat wt:'", raw)
	}
}

// removeWorktreeLeaf removes the leaf by its name through a handle of the parent
// that it opened. The hook moves that parent away and puts a new parent with an
// empty leaf at the old path. The leaf inside the opened parent goes, and the new
// one stays. A removal by the full path takes the new leaf instead. A parent of mode
// 0300 gives the handle, and its empty leaf goes. A file at the leaf goes, a link
// goes as a link and its target stays, and a folder with an entry stays. All paths
// are in a t.TempDir().
//
// Mutations: an unlinkat with AT_REMOVEDIR alone keeps the file and the link
// (removes_a_file, removes_a_link_only). No AT_REMOVEDIR call after EISDIR keeps
// the empty leaf (the_opened_parent). os.RemoveAll of the leaf takes the folder
// with its entry (keeps_a_folder_with_an_entry).
func TestRemoveWorktreeLeafRemovesThroughTheParentHandle(t *testing.T) {
	t.Run("the_opened_parent", func(t *testing.T) {
		dir := t.TempDir()
		parent := filepath.Join(dir, "p")
		moved := filepath.Join(dir, "moved")
		leaf := filepath.Join(parent, "w1")
		if err := os.MkdirAll(leaf, 0o755); err != nil {
			t.Fatal(err)
		}
		leafParentOpenedHook = func() {
			if err := os.Rename(parent, moved); err != nil {
				t.Error(err)
			}
			if err := os.MkdirAll(leaf, 0o755); err != nil {
				t.Error(err)
			}
		}
		t.Cleanup(func() { leafParentOpenedHook = nil })
		if err := removeWorktreeLeaf(leaf+"/", nil); err != nil {
			t.Fatalf("removeWorktreeLeaf: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(moved, "w1")); !os.IsNotExist(err) {
			t.Errorf("the leaf in the opened parent survived: Lstat err %v", err)
		}
		if _, err := os.Lstat(leaf); err != nil {
			t.Errorf("the leaf in the new parent went: %v", err)
		}
	})
	t.Run("a_parent_with_no_read_permission", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a folder of mode 0300")
		}
		parent := filepath.Join(t.TempDir(), "p")
		leaf := filepath.Join(parent, "w1")
		if err := os.MkdirAll(leaf, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
		if err := removeWorktreeLeaf(leaf, nil); err != nil {
			t.Fatalf("removeWorktreeLeaf in a parent of mode 0300: %v", err)
		}
		if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
			t.Errorf("the leaf in a parent of mode 0300 survived: Lstat err %v", err)
		}
	})
	t.Run("removes_a_file", func(t *testing.T) {
		leaf := filepath.Join(t.TempDir(), "w1")
		if err := os.WriteFile(leaf, []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := removeWorktreeLeaf(leaf, nil); err != nil {
			t.Errorf("removeWorktreeLeaf on a file: %v", err)
		}
		if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
			t.Errorf("the file at the leaf survived: Lstat err %v", err)
		}
	})
	t.Run("removes_a_link_only", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "tg")
		leaf := filepath.Join(dir, "p", "w1")
		writeFile(t, filepath.Join(target, "keep.txt"), "keep\n", 0o644)
		if err := os.MkdirAll(filepath.Dir(leaf), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, leaf); err != nil {
			t.Fatal(err)
		}
		if err := removeWorktreeLeaf(leaf, nil); err != nil {
			t.Errorf("removeWorktreeLeaf on a link: %v", err)
		}
		if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
			t.Errorf("the link at the leaf survived: Lstat err %v", err)
		}
		if b, err := os.ReadFile(filepath.Join(target, "keep.txt")); err != nil || string(b) != "keep\n" {
			t.Errorf("the target of the link lost keep.txt: %q, %v", b, err)
		}
	})
	t.Run("keeps_a_folder_with_an_entry", func(t *testing.T) {
		leaf := filepath.Join(t.TempDir(), "w1")
		writeFile(t, filepath.Join(leaf, ".git"), "gitdir: x\n", 0o644)
		if err := removeWorktreeLeaf(leaf, nil); err == nil {
			t.Error("removeWorktreeLeaf on a folder with an entry: no error")
		}
		if _, err := os.Stat(filepath.Join(leaf, ".git")); err != nil {
			t.Errorf("the folder at the leaf lost its entry: %v", err)
		}
	})
}

// undoFailedAdd after a failed add removes a file or a link that took the place
// of the leaf. The link goes as a link, and its target stays. c9125eb8 and
// 4c111e86 do so on Linux (measured). All paths are in a t.TempDir(). Mutation:
// compare leafResolve of the leaf in undoFailedAdd, not failedAddLeafResolve (the
// link stays).
func TestUndoFailedAddRemovesAnEntryInPlaceOfTheLeaf(t *testing.T) {
	for _, kind := range []string{"file", "link"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			wt := filepath.Join(dir, "p", "w1")
			target := filepath.Join(dir, "tg")
			writeFile(t, filepath.Join(target, "keep.txt"), "keep\n", 0o644)
			if err := os.MkdirAll(wt, 0o755); err != nil {
				t.Fatal(err)
			}
			cp := checkpointCreatedWorktree(wt)
			defer cp.release()
			if cp.info == nil {
				t.Fatal("checkpoint did not capture the leaf")
			}
			if err := os.Remove(wt); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == "file" {
				err = os.WriteFile(wt, []byte("hello\n"), 0o644)
			} else {
				err = os.Symlink(target, wt)
			}
			if err != nil {
				t.Fatal(err)
			}
			undoFailedAdd(wt, cp)
			if _, err := os.Lstat(wt); !os.IsNotExist(err) {
				t.Errorf("the %s at the leaf survived: Lstat err %v", kind, err)
			}
			if b, err := os.ReadFile(filepath.Join(target, "keep.txt")); err != nil || string(b) != "keep\n" {
				t.Errorf("the target folder lost keep.txt: %q, %v", b, err)
			}
		})
	}
}

// undoFailedAdd with an empty checkpoint, or with one that holds no identity of the
// parent, keeps the old rule: a file or a link at the leaf stays, and an empty folder
// goes (claustrum's own, not measured). All paths are in a t.TempDir(). Mutation:
// call removeWorktreeLeaf there in place of rmdirWorktreeLeaf (the file and the
// link go).
func TestUndoFailedAddEmptyCheckpointRemovesOnlyAFolder(t *testing.T) {
	for _, cpKind := range []string{"empty", "no_parent"} {
		for _, kind := range []string{"file", "link", "folder"} {
			t.Run(cpKind+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				wt := filepath.Join(dir, "p", "w1")
				target := filepath.Join(dir, "tg")
				writeFile(t, filepath.Join(target, "keep.txt"), "keep\n", 0o644)
				if err := os.MkdirAll(wt, 0o755); err != nil {
					t.Fatal(err)
				}
				var cp worktreeCheckpoint
				if cpKind == "no_parent" {
					cp = checkpointCreatedWorktree(wt)
					defer cp.release()
					if cp.info == nil {
						t.Fatal("checkpoint did not capture the leaf")
					}
					cp.parentInfo = nil
				}
				if kind != "folder" {
					if err := os.Remove(wt); err != nil {
						t.Fatal(err)
					}
					var err error
					if kind == "file" {
						err = os.WriteFile(wt, []byte("hello\n"), 0o644)
					} else {
						err = os.Symlink(target, wt)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				undoFailedAdd(wt, cp)
				_, err := os.Lstat(wt)
				if kind == "folder" && !os.IsNotExist(err) {
					t.Errorf("the empty folder at the leaf survived: Lstat err %v", err)
				}
				if kind != "folder" && err != nil {
					t.Errorf("the %s at the leaf went: %v", kind, err)
				}
				if b, err := os.ReadFile(filepath.Join(target, "keep.txt")); err != nil || string(b) != "keep\n" {
					t.Errorf("the target folder lost keep.txt: %q, %v", b, err)
				}
			})
		}
	}
}

// undoFailedAdd tests the parent folder, and removeWorktreeLeaf then opens it. The
// hook swaps the parent between the two: it moves the tested parent away and puts
// a new parent with a file or a link at the leaf name. The opened handle is not the
// tested parent, so the entry in the new parent stays. This guard is claustrum's
// own (not measured). All paths are in a t.TempDir(). Mutation: drop the handleIs
// test in removeWorktreeLeaf (the entry goes).
func TestUndoFailedAddKeepsAnEntryOfASwappedParent(t *testing.T) {
	for _, kind := range []string{"file", "link"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			parent := filepath.Join(dir, "p")
			moved := filepath.Join(dir, "moved")
			target := filepath.Join(dir, "tg")
			wt := filepath.Join(parent, "w1")
			writeFile(t, filepath.Join(target, "keep.txt"), "keep\n", 0o644)
			if err := os.MkdirAll(wt, 0o755); err != nil {
				t.Fatal(err)
			}
			cp := checkpointCreatedWorktree(wt)
			defer cp.release()
			if cp.info == nil || cp.parentInfo == nil {
				t.Fatal("checkpoint did not capture the leaf and its parent")
			}
			leafParentOpenHook = func() {
				if err := os.Rename(parent, moved); err != nil {
					t.Error(err)
				}
				if err := os.Mkdir(parent, 0o755); err != nil {
					t.Error(err)
				}
				var err error
				if kind == "file" {
					err = os.WriteFile(wt, []byte("hello\n"), 0o644)
				} else {
					err = os.Symlink(target, wt)
				}
				if err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(func() { leafParentOpenHook = nil })
			undoFailedAdd(wt, cp)
			if _, err := os.Lstat(wt); err != nil {
				t.Errorf("the %s in the swapped-in parent went: %v", kind, err)
			}
			if b, err := os.ReadFile(filepath.Join(target, "keep.txt")); err != nil || string(b) != "keep\n" {
				t.Errorf("the target folder lost keep.txt: %q, %v", b, err)
			}
		})
	}
}
