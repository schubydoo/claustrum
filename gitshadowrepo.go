package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// The folder of one repository in the folder of the daemon, as 5fd08069 keeps
// it: <S>/<32 hex>. It holds the daemon's own copy of the settings of the
// repository and of a few more files of its git directory. The git calls of a
// request name it in their variables (gitUse).
//
// Every write goes through an os.Root at the folder of the daemon or at the
// folder of the repository, with a name that the daemon builds itself. Every
// delete goes through shadowHost.removeEntry.

// shadowHeadMaxBytes is the cut of the copy of HEAD. On 5fd08069 a HEAD of 8191
// or 8192 bytes is copied whole, and one of 8193 bytes or of 1 MiB gives a copy
// of 8192 bytes (cells 17a to 17k of s3p2b-edges-linux, Linux VM).
const shadowHeadMaxBytes = 8192

// shadowCopiedFiles are the files of the git directory that get a copy when
// they exist, beside HEAD and the config. Each copy follows its file: it is
// written again when the file has another text, and it goes when the file is
// gone (cells 19a to 19e of s3p2b-edges-linux, Linux VM, and cell 15 of
// s3p2b-edges2 on macOS and Windows VMs). `rebase-apply/head-name` ran on a
// Linux VM only (cell 20a), and it is copied on every system here.
//
// 5fd08069 makes no entry for packed-refs, MERGE_HEAD, CHERRY_PICK_HEAD,
// REVERT_HEAD, sequencer, ORIG_HEAD, FETCH_HEAD, info/grafts,
// info/sparse-checkout and objects/info/alternates (cells B3 of s3p2-linux and
// 20b to 20i of s3p2b-edges-linux).
var shadowCopiedFiles = []string{
	"info/exclude",
	"info/attributes",
	"shallow",
	"rebase-merge/head-name",
	"rebase-apply/head-name",
	"BISECT_START",
}

// shadowRepo is what the daemon knows of one folder <S>/<32 hex> that it made.
type shadowRepo struct {
	// read is the read of the config whose text is in the copy.
	read *repoVerdict
	// copies has the sha256 of the content of each copy that is there.
	copies map[string][sha256.Size]byte
}

// shadowRepoName is the name of the folder of the repository with the git
// directory gitDir: the first 32 hex digits of the sha256 of that path, with no
// trailing separator.
//
// On 5fd08069 the path is `<repo>/.git` for a plain repository (s3p2-linux cell
// B1, Linux VM, and TestShadowRepoName for the digits of that cell). A bare repository gives the path of the
// repository itself (cell B3c). A request on a linked worktree gives the folder
// of its repository (cell B3b). A `.git` file gives the git directory that it
// names, and a submodule its folder under `modules` (cells 20j and 23a of
// s3p2b-edges-linux). On Linux and macOS a symbolic link in the path is
// resolved, and the letter case is as sent (s3p2-macos cells B1 and B2). On a
// Windows VM the path has backslashes, a junction is not resolved and another
// letter case gives the same name (s3p2-windows cells B1, B2 and D6j).
//
// gitDir is the git directory that the trust check pinned. That check resolves
// the links of the request path on Linux and macOS, and on Windows it resolves
// no junction and gives each name the letter case of the disk (walkStart). No
// Windows or macOS VM ran claustrum with this name yet.
func shadowRepoName(gitDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(gitDir)))
	return hex.EncodeToString(sum[:])[:32]
}

// readShadowSource reads the file at path for a copy. ok is false for a file
// that is missing, that is not a regular file, or that the daemon cannot read.
// A symbolic link gets no copy: what 5fd08069 copies for one is not measured.
// The open does not wait and takes no terminal (openRepoFile). The file that
// was opened must be the one that the first look saw.
//
// With cut, the content is the first max bytes. Without it, a file of more
// than max bytes gets no copy. The config gate refused such a file of the
// three that it tests before this code runs (gitDirFilesDetail). For the
// others the bound is claustrum's own.
func readShadowSource(path string, max int64, cut bool) (data []byte, ok bool) {
	seen, err := os.Lstat(path)
	if err != nil || !seen.Mode().IsRegular() {
		return nil, false
	}
	f, err := openRepoFile(path)
	if err != nil || f == nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !os.SameFile(seen, fi) {
		return nil, false
	}
	limit := max
	if !cut {
		limit = max + 1
	}
	data, err = io.ReadAll(io.LimitReader(f, limit))
	if err != nil || int64(len(data)) > max {
		return nil, false
	}
	return data, true
}

// shadowHeadLinkPrefix starts the target of a HEAD that is a symbolic link and
// still gets a copy (readShadowHead).
const shadowHeadLinkPrefix = "refs/"

// readShadowHead is the content of the copy of the HEAD of gitDir.
//
// A HEAD that is a regular file gives its first shadowHeadMaxBytes bytes. A
// HEAD that is a symbolic link to `refs/heads/main` gives a regular file with
// the text `ref: refs/heads/main` and a newline, 21 bytes: "ref: ", the target
// of the link as it is written, and a newline. 5fd08069 writes that copy and
// answers all five methods normally (cells N4-headlink of val-s3p2b2-3-linux2
// and val-s3p2b2-3-macos, Linux and macOS VMs).
//
// A link to `refs/remotes/origin/<name>` gets its copy the same way. 5fd08069
// writes `ref: refs/remotes/origin/main` and a newline, 30 bytes, and goes on.
// It does so for `refs/remotes/origin/nope` too, a ref that does not exist.
// The methods then answer by what git says there (cell H1 of
// val-s3p2b2-7-linux2, Linux VM, 1 run). A link to `refs/tags/v1` gives the
// copy `ref: refs/tags/v1` and a newline, and a link to `refs/x` gives `ref:
// refs/x` and a newline (cell N-e of val-s3p2b2-8-linux2, Linux VM, 1 run
// each). A target that does
// not start with `refs/`, such as an absolute path, is not measured. The trust
// check finds no repository there, so such a HEAD does not come here. If it
// does, it gets no copy and the request is refused.
func readShadowHead(gitDir string) ([]byte, bool) {
	path := filepath.Join(gitDir, "HEAD")
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil || !strings.HasPrefix(target, shadowHeadLinkPrefix) || len(target) > shadowHeadMaxBytes {
			return nil, false
		}
		return []byte("ref: " + target + "\n"), true
	}
	return readShadowSource(path, shadowHeadMaxBytes, true)
}

// syncRepo makes the folder of the repository with the git directory gitDir
// when it is not there, and brings its copies up to date. read is the accepted
// read of the config of that repository.
//
// 5fd08069 makes the folder at the first request that accepts the repository.
// git.info, git.status and git.worktree_create make it alike (s3-macos section
// 1 and s3b-linux cell W1). git.list_branches and git.worktree_remove ran as
// later requests only. They name the folder in their git calls, so they make
// it here too (not measured as a first request). A refused config gives no
// folder (s3p2-linux section 4).
//
// The entries, with the modes of Linux and macOS (s3-linux cell A3, s3p2-linux
// cells B3, Linux VM. Cell 24 of s3p2b-edges on macOS and Windows VMs):
//
//   - HEAD (0600): the first shadowHeadMaxBytes bytes of the HEAD of gitDir.
//   - config (0600): the text of the read (shadowConfigText).
//   - info, objects and refs (0700). objects and refs stay empty.
//   - worktrees: a symbolic link to <gitDir>/worktrees, also when that folder
//     does not exist. Windows has no such entry (shadowLinksWorktrees).
//   - the copies of shadowCopiedFiles (0600), in folders of 0700.
//   - BISECT_LOG (0600): an empty file while the repository has that file.
//   - reftable: a link to <gitDir>/reftable while that is a folder. A macOS VM
//     with git 2.50.1 showed it (cell 25 of s3p2b-edges). Linux did not run it
//     and gets the same link here. Windows gets none, as for worktrees (not
//     measured).
//
// A second request with nothing changed writes nothing: inode, times and size
// of every entry stay (s3p2-linux cell B4). A copy is written again as a new
// file, through a temporary file and a rename, when its text differs from the
// text of the last write. The config is written again after each new read of
// the repository's config (repoReadVerdict has the rule of a new read). A
// `touch` of the config with the same text makes a new read. Whether 5fd08069
// writes a new file then is not measured: claustrum does.
//
// It returns how the git calls of the request name the repository: the folder
// that it made or checked. An error leaves the folder as far as it got, and
// the request must not go on to a git call (settingsGateRefusal). A HEAD that
// cannot be read makes no folder. A daemon in its shutdown, and a folder of
// the daemon that is gone and cannot be made again, give an error too. None of
// the three is measured.
func (h *shadowHost) syncRepo(gitDir string, read *repoVerdict) (gitUse, error) {
	h.repoMu.Lock()
	defer h.repoMu.Unlock()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return gitUse{}, errors.New("the daemon is in its shutdown")
	}
	if h.dir == "" {
		if err := h.makeFolder(); err != nil {
			h.mu.Unlock()
			return gitUse{}, err
		}
	}
	dir := h.dir
	h.mu.Unlock()
	name := shadowRepoName(gitDir)
	use := gitUse{host: h, folder: dir, gitDir: gitDir, repoDir: filepath.Join(dir, name),
		items: read.worktreeItems, shared: read.shared}
	return use, h.syncRepoFolder(dir, name, gitDir, read)
}

// syncRepoFolder is the work of syncRepo on the folder name in dir, the folder
// of the daemon. The caller holds repoMu.
func (h *shadowHost) syncRepoFolder(dir, name, gitDir string, read *repoVerdict) error {
	if h.repos == nil {
		h.repos = map[string]*shadowRepo{}
	}
	head, ok := readShadowHead(gitDir)
	if !ok {
		return errors.New("the HEAD of the repository cannot be read")
	}
	base, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = base.Close() }()

	// A folder that is not there is the first request, or a folder that went.
	// The copies then come from what the daemon read before: the config is not
	// read again for it (cell 54 of s3p2b-edges3-linux2, Linux VM).
	//
	// The error of a folder that cannot be made names its full path, as on
	// 5fd08069: `mkdir <S>/<32 hex>: permission denied` under a folder of mode
	// 0500, and `mkdir <S>/<32 hex>: not a directory` for a plain file of that
	// name (cells N4 of val-s3p2b2-3-linux2 and val-s3p2b2-3-macos, Linux and
	// macOS VMs). On a Windows VM the file gives `mkdir <path>: The system cannot
	// find the path specified.` (cells N4 of val-s3p2b2-3-windows).
	//
	// The folder is made by its name through the root at the folder of the
	// daemon, and never with its parents. A shutdown that removed the folder of
	// the daemon after the test of syncRepo then leaves no folder behind: the
	// mkdir fails (not measured, and no client gets that reply).
	st := h.repos[name]
	path := filepath.Join(dir, name)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		st = nil
	}
	shadowRepoBeforeMkdir()
	if err := mkdirShadowRepo(base, name, path); err != nil {
		return err
	}
	root, err := base.OpenRoot(name)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if st == nil {
		st = &shadowRepo{copies: map[string][sha256.Size]byte{}}
		h.repos[name] = st
	}
	rel := filepath.Join(filepath.Base(dir), name)

	for _, sub := range []string{"info", "objects", "refs"} {
		if err := root.Mkdir(sub, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	if shadowLinksWorktrees {
		if err := shadowLink(root, filepath.Join(gitDir, worktreesSubdir), worktreesSubdir); err != nil {
			return err
		}
		if fi, err := os.Lstat(filepath.Join(gitDir, "reftable")); err == nil && fi.IsDir() {
			if err := shadowLink(root, filepath.Join(gitDir, "reftable"), "reftable"); err != nil {
				return err
			}
		}
	}
	if err := h.writeCopy(st, root, rel, "HEAD", head); err != nil {
		return err
	}
	if st.read != read {
		delete(st.copies, "config")
		if err := h.writeCopy(st, root, rel, "config", []byte(read.shadowText)); err != nil {
			return err
		}
		st.read = read
	}
	for _, file := range shadowCopiedFiles {
		data, ok := readShadowSource(filepath.Join(gitDir, filepath.FromSlash(file)), repoGitFileMaxBytes, false)
		if ok {
			if err := h.writeCopy(st, root, rel, file, data); err != nil {
				return err
			}
			continue
		}
		if err := h.dropCopy(st, rel, file); err != nil {
			return err
		}
	}
	// BISECT_LOG is an empty file where the repository's file has 340 or 348
	// bytes (cells B3i of s3p2-linux, and cell 24 i of s3p2b-edges on macOS and
	// Windows VMs).
	if fi, err := os.Lstat(filepath.Join(gitDir, "BISECT_LOG")); err == nil && fi.Mode().IsRegular() {
		return h.writeCopy(st, root, rel, "BISECT_LOG", nil)
	}
	return h.dropCopy(st, rel, "BISECT_LOG")
}

// shadowRepoBeforeMkdir is a seam for the tests. It runs right before the mkdir
// of the folder of a repository. Production never reassigns it.
var shadowRepoBeforeMkdir = func() {}

// mkdirShadowRepo makes the folder name in base, the root at the folder of the
// daemon, with mode 0700. A folder that is there passes. path is the full path
// of the folder: an error names it, with the operation `mkdir`. Another entry
// at that name gives the error of os.MkdirAll for a path that is no folder.
func mkdirShadowRepo(base *os.Root, name, path string) error {
	err := base.Mkdir(name, 0o700)
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		fi, serr := base.Stat(name)
		if serr == nil && fi.IsDir() {
			return nil
		}
		err = syscall.ENOTDIR
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return &os.PathError{Op: "mkdir", Path: path, Err: err}
}

// shadowLink makes the symbolic link name in root with the target, when no
// entry of that name is there. The link points out of the folder by design.
// Nothing here follows it.
func shadowLink(root *os.Root, target, name string) error {
	if _, err := root.Lstat(name); err == nil {
		return nil
	}
	return root.Symlink(target, name)
}

// writeCopy makes file in root, the folder rel of a repository, with the
// content data, when the copy that is there has another content or there is
// none. The copy is a new file each time (writeNewFile). A temporary file that
// is left after an error goes through removeEntry.
func (h *shadowHost) writeCopy(st *shadowRepo, root *os.Root, rel, file string, data []byte) error {
	sum := sha256.Sum256(data)
	if have, ok := st.copies[file]; ok && have == sum {
		return nil
	}
	path := filepath.FromSlash(file)
	if dir := filepath.Dir(path); dir != "." {
		if err := root.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	if left, err := writeNewFile(root, path, data); err != nil {
		if left != "" {
			_ = h.removeEntry(filepath.Join(rel, left))
		}
		return err
	}
	st.copies[file] = sum
	return nil
}

// writeNewFile makes the file path in root with the content data as a new
// file: a temporary file `.tmp-<digits>` (mode 0600) in the folder of the file,
// then a rename. 5fd08069 writes each copy so. A poll saw
// `<S>/<32 hex>/.tmp-<digits>`, `<S>/<32 hex>/info/.tmp-<digits>`,
// `<S>/.check-<n>/shadow/.tmp-<digits>` and `<S>/.probe/.tmp-<digits>` with
// mode 0600 and 7 to 10 decimal digits (val-s3p2b2-3-macos row D7, macOS VM).
// The copy has a new inode after each write (cells 18a, 18k and 19c of
// s3p2b-edges-linux, and row D5 of val-s3p2b2-3-linux2 for the folder of the
// start test, Linux VM).
//
// After an error, left is the name of a temporary file that is still there, or
// "".
func writeNewFile(root *os.Root, path string, data []byte) (left string, err error) {
	var n [4]byte
	if _, err := rand.Read(n[:]); err != nil {
		return "", err
	}
	tmp := filepath.Join(filepath.Dir(path), ".tmp-"+strconv.FormatUint(uint64(binary.LittleEndian.Uint32(n[:])), 10))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, path)
	}
	if err != nil {
		return tmp, err
	}
	return "", nil
}

// dropCopy removes the copy file of the repository folder rel, a path below
// the folder of all daemons, when the daemon made one. The copy of a file in a
// rebase folder goes with its folder: after `rebase-merge/head-name` went on
// 5fd08069, the folder and the file are gone. `info` stays, empty (cells 19c
// and 19d of s3p2b-edges-linux, Linux VM).
func (h *shadowHost) dropCopy(st *shadowRepo, rel, file string) error {
	if _, ok := st.copies[file]; !ok {
		return nil
	}
	target := filepath.FromSlash(file)
	if dir := filepath.Dir(target); dir != "." && dir != "info" {
		target = dir
	}
	if err := h.removeEntry(filepath.Join(rel, target)); err != nil {
		return err
	}
	delete(st.copies, file)
	return nil
}

// listEntryConfig is the read of the `config.worktree` file of a linked
// worktree in a git.info request. On 5fd08069 a git.info on a linked worktree
// lists that file with one git call in each request, when the repository has
// the worktreeConfig switch and the file exists. Its settings are in no copy
// (cell 1c of s3p2b-edges2-linux, Linux VM). git.status lists the file in its
// own test of the entry (statusEntryConfig), and git.list_branches does not
// list it (the same cell). The settings of the listing go to the calls of the
// request on that worktree as `-c` items (gitUse.front).
func (req *request) listEntryConfig(t gitDirTrust) {
	if req.repoRead == nil || !req.repoRead.worktreeConfig || t.worktreeEntry == "" {
		return
	}
	ctx, cancel := gitCtx()
	defer cancel()
	settings, _ := listConfigFile(ctx, filepath.Join(t.worktreeEntry, worktreeConfigName))
	req.use.entryItems = worktreeConfigItems(settings)
}
