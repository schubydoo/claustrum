//go:build !windows

package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The texts of worktree_lfs_not_kept after Git LFS downloads began, and the
// notes after the text of a long copy, from the replies of 4c111e86 (measured
// on Linux), with the path p of the link.
func notKeptLinkWant(objects bool, p string) string {
	name := "folder"
	if objects {
		name = "objects folder"
	}
	return "The repository's Git LFS " + name + " on the remote machine, `" + p + "`, is a symbolic link. If you didn't make that link," +
		" remove only the link, not the folder it points to: run rm '" + p + "' (exactly so, with no -r: that way rm removes a link but" +
		" refuses a real folder), then try again. If you did make it, turn off Worktree for this session instead. A plain retry won't" +
		" help: git worktree add timed out after 4 seconds once Git LFS downloads had begun, and Claude can't keep the downloaded files" +
		" in a linked folder for the next try."
}

const notKeptRoomWant = "Free up space on the disk that holds the repository on the remote machine (or raise your quota there) until" +
	" the repository's Git LFS files fit with at least 1 GiB to spare, or turn off Worktree for this session, then try again. A plain" +
	" retry won't help: git worktree add timed out after 4 seconds once Git LFS downloads had begun, and there is too little room" +
	" left on the disk that holds the repository, or in your disk quota there, so Claude can't keep the downloaded files for the next try."

func notKeptNoteWant(objects bool, p string) string {
	name := "folder"
	if objects {
		name = "objects folder"
	}
	return " Also, the repository's Git LFS " + name + " on the remote machine, `" + p + "`, is a symbolic link. If you didn't make" +
		" that link, remove just the link: run rm '" + p + "' (exactly so, with no -r: that way rm removes a link but refuses a real folder)."
}

// TestNotKeptAfterDownloadsBegan: a deadline after Git LFS downloads began
// answers worktree_lfs_not_kept when the keep left a downloaded file out
// because of a link at `lfs` (text A) or at `lfs/objects` (text B), or too
// little room on the disk of the repository (text C). A kept object gives no
// text (4c111e86, measured on Linux). After the text of a long copy, a link
// adds a note (E and F, measured on Linux). The tests of the start of the
// downloads and of the bound of the room are claustrum's own.
func TestNotKeptAfterDownloadsBegan(t *testing.T) {
	const mib = uint64(1) << 20
	oid := strings.Repeat("ab", 32)
	for _, row := range []struct {
		name       string
		link       string // "", "lfs" or "objects"
		kept       bool   // the store of the repository has the object
		downloaded bool   // the private store has the object
		incomplete bool
		copied     time.Duration
		free       uint64
		want       func(gitDir string) string
		body       string // the object, or "" for a body of 19 bytes
	}{
		{"lfs link", "lfs", false, true, true, 0, 1 << 40, func(g string) string { return notKeptLinkWant(false, filepath.Join(g, "lfs")) }, ""},
		{"objects link", "objects", false, true, true, 0, 1 << 40, func(g string) string { return notKeptLinkWant(true, filepath.Join(g, "lfs", "objects")) }, ""},
		{"lfs link, a download done", "lfs", false, true, false, 0, 1 << 40, func(g string) string { return notKeptLinkWant(false, filepath.Join(g, "lfs")) }, ""},
		{"free 8 MiB", "", false, true, true, 0, 8 * mib, func(string) string { return notKeptRoomWant }, ""},
		{"free 400 MiB", "", false, true, true, 0, 400 * mib, func(string) string { return notKeptRoomWant }, ""},
		{"enough room, left out", "", false, true, true, 0, 1<<30 + 6*mib, nil, ""},
		{"object kept, little room", "", true, true, true, 0, 8 * mib, nil, ""},
		{"object kept", "", true, true, true, 0, 1 << 40, nil, ""},
		{"link, no download began", "lfs", false, false, false, 0, 1 << 40, nil, ""},
		{"room, no file left out", "", false, false, true, 0, 8 * mib, nil, ""},
		{"long copy, lfs link", "lfs", false, false, false, time.Second, 1 << 40, func(g string) string {
			return notKeptWant4s + notKeptNoteWant(false, filepath.Join(g, "lfs"))
		}, ""},
		{"long copy, objects link", "objects", false, false, false, time.Second, 1 << 40, func(g string) string {
			return notKeptWant4s + notKeptNoteWant(true, filepath.Join(g, "lfs", "objects"))
		}, ""},
		{"long copy, no link", "", false, false, false, time.Second, 8 * mib, func(string) string { return notKeptWant4s }, ""},
		{"free 10 bytes over the spare, a larger file", "", false, true, true, 0, 1<<30 + 10, func(string) string { return notKeptRoomWant }, ""},
		{"free 10 bytes over the spare, a smaller file", "", false, true, true, 0, 1<<30 + 10, nil, "tiny"},
		{"a link and little room", "lfs", false, true, true, 0, 8 * mib, func(g string) string { return notKeptLinkWant(false, filepath.Join(g, "lfs")) }, ""},
		{"both links", "both", false, true, true, 0, 1 << 40, func(g string) string { return notKeptLinkWant(false, filepath.Join(g, "lfs")) }, ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			gitDir := filepath.Join(t.TempDir(), ".git")
			target := t.TempDir()
			lfs := filepath.Join(gitDir, "lfs")
			switch row.link {
			case "lfs":
				mkdirAll(t, filepath.Join(target, "objects"))
				mkdirAll(t, gitDir)
				symlink(t, target, lfs)
			case "objects":
				mkdirAll(t, lfs)
				symlink(t, target, filepath.Join(lfs, "objects"))
			case "both":
				objects := t.TempDir()
				mkdirAll(t, gitDir)
				symlink(t, objects, filepath.Join(target, "objects"))
				symlink(t, target, lfs)
			default:
				mkdirAll(t, filepath.Join(lfs, "objects"))
			}
			body := "a downloaded object"
			if row.body != "" {
				body = row.body
			}
			if row.kept {
				mustWrite(t, storeObjectPath(lfs, oid), body)
			}
			private := filepath.Join(t.TempDir(), "lfs")
			mkdirAll(t, private)
			if row.downloaded {
				mustWrite(t, storeObjectPath(private, oid), body)
			}
			if row.incomplete {
				mkdirAll(t, filepath.Join(private, "incomplete"))
			}
			prev := storeFreeBytes
			storeFreeBytes = func(p string) (uint64, bool) {
				if pathUnder(p, gitDir) {
					return row.free, true
				}
				return 1 << 40, true
			}
			t.Cleanup(func() { storeFreeBytes = prev })
			rule := &filterRule{store: &storeUse{path: private, copied: row.copied}}
			got, ok := notKeptText(4000, rule, gitDir)
			want := ""
			if row.want != nil {
				want = row.want(gitDir)
			}
			if ok != (want != "") || got != want {
				t.Errorf("notKeptText = %q, %v\nwant %q", got, ok, want)
			}
		})
	}
}

// notKeptWant4s is the text of a long copy of the seed with timeoutMs 4000
// (4c111e86, measured on Linux and macOS).
const notKeptWant4s = "Turn off Worktree for this session, then try again. A plain retry won't help: git worktree add timed out after 4 seconds," +
	" and copying the repository's Git LFS files into a folder of Claude's own took a large part of that time, as it does when Claude" +
	" can't hard-link them there (a repository on another disk, a store reached through a link, a relative lfs.storage); a retry copies them again."

func mkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestLFSNotKeptAfterDownloadsBegan: a create whose timeoutMs ends while
// git-lfs downloads b.bin, after it downloaded a.bin into the private store.
// A link at `lfs` or `lfs/objects`, or too little room on the disk of the
// repository, keeps a.bin out, and the reply is worktree_lfs_not_kept. With
// room and no link a.bin is kept, and the reply is timeout (4c111e86, measured
// on Linux).
func TestLFSNotKeptAfterDownloadsBegan(t *testing.T) {
	const mib = uint64(1) << 20
	for _, row := range []struct {
		name string
		link string
		free uint64
		code string
	}{
		{"lfs link", "lfs", 1 << 40, "worktree_lfs_not_kept"},
		{"objects link", "objects", 1 << 40, "worktree_lfs_not_kept"},
		{"free 8 MiB", "", 8 * mib, "worktree_lfs_not_kept"},
		{"plain", "", 1 << 40, "timeout"},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
			f.onServer(t, "a.bin")
			mustWrite(t, filepath.Join(f.remote, "hang"), "")
			mustWrite(t, filepath.Join(f.remote, "incomplete"), "")
			lfs := filepath.Join(f.gitDir, "lfs")
			want := ""
			switch row.link {
			case "lfs":
				target := t.TempDir()
				mkdirAll(t, filepath.Join(target, "objects"))
				symlink(t, target, lfs)
				want = notKeptLinkWantMs(false, lfs)
			case "objects":
				target := t.TempDir()
				mkdirAll(t, lfs)
				symlink(t, target, filepath.Join(lfs, "objects"))
				want = notKeptLinkWantMs(true, filepath.Join(lfs, "objects"))
			default:
				if row.code != "timeout" {
					want = strings.Replace(notKeptRoomWant, "4 seconds", "5 seconds", 1)
				}
			}
			prev := storeFreeBytes
			storeFreeBytes = func(p string) (uint64, bool) {
				if pathUnder(p, f.gitDir) {
					return row.free, true
				}
				return 1 << 40, true
			}
			t.Cleanup(func() { storeFreeBytes = prev })
			raw, _ := f.create(t, map[string]any{"timeoutMs": 5000})
			if !strings.Contains(raw, `"errorCode":"`+row.code+`"`) {
				t.Errorf("git.worktree_create = %s\nwant errorCode %s", raw, row.code)
			}
			if want != "" && !strings.Contains(raw, `"error":`+jsonQuoted(t, want)+`,`) {
				t.Errorf("git.worktree_create = %s\nwant the text %q", raw, want)
			}
			kept := exists(storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids["a.bin"]))
			if kept != (row.code == "timeout") {
				t.Errorf("a.bin in the store of the repository = %v, want %v", kept, row.code == "timeout")
			}
			f.wantGone(t)
		})
	}
}

// notKeptLinkWantMs is notKeptLinkWant with timeoutMs 5000.
func notKeptLinkWantMs(objects bool, p string) string {
	return strings.Replace(notKeptLinkWant(objects, p), "4 seconds", "5 seconds", 1)
}

// pathUnder reports whether p is dir or below it, as given or after
// filepath.EvalSymlinks of both sides. On macOS a temp folder in /var is
// /private/var after the links. A path that does not resolve is compared as
// given.
func pathUnder(p, dir string) bool {
	under := func(p, dir string) bool {
		return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
	}
	if under(p, dir) {
		return true
	}
	rp, err1 := filepath.EvalSymlinks(p)
	rd, err2 := filepath.EvalSymlinks(dir)
	return err1 == nil && err2 == nil && under(rp, rd)
}

// TestLFSCheckoutTextEndsBeforeLog: the text of a failed LFS checkout with
// the private store ends before the line of git-lfs that names its log file. The
// ssh note follows (4c111e86, measured on Linux with git-lfs, row b of a
// server error).
func TestLFSCheckoutTextEndsBeforeLog(t *testing.T) {
	f := newNamedLFSFixture(t, "lfs", map[string]string{"b.bin": "beta-object"})
	failure := "Downloading b.bin (11 B)\nError downloading object: b.bin (2a2a2a2): Smudge error: Error downloading b.bin: batch response:" +
		" Fatal error: Server error: http://127.0.0.1:9/lfs/objects/batch\n"
	mustWrite(t, filepath.Join(f.remote, "error"), failure+
		"\nErrors logged to '<store>/logs/20261115T010203.log'.\nUse `git lfs logs last` to view the log.\n")
	raw, _ := f.create(t, nil)
	want := "git worktree add failed (checkout): Downloading b.bin (11 B) Error downloading object: b.bin (2a2a2a2): Smudge error:" +
		" Error downloading b.bin: batch response: Fatal error: Server error: http://127.0.0.1:9/lfs/objects/batch. Claude doesn't use ssh," +
		" credential, or proxy settings from this repository's own git config. If the download needs one and you trust that setting," +
		" copy it into ~/.gitconfig on the remote machine, then try again."
	if !strings.HasSuffix(raw, `"error":`+jsonQuoted(t, want)+`,"errorCode":"worktree_add_failed"}}`) {
		t.Errorf("git.worktree_create = %s\nwant the text %q", raw, want)
	}
	f.wantGone(t)
}
