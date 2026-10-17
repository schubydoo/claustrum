package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// gitUse says how the git calls of one request name their repository.
//
// The zero value is the form that claustrum had before: a call carries
// GIT_COMMON_DIR=<git directory> and reads the repository's own config. It is
// the form of a request with "shadowDirOff":true, of the state fallback-git and
// of a server with no run folder. 5fd08069 sends that one folder variable with
// the member (val-s3p2b1 section 1.2, Linux, macOS and Windows VMs). It sends
// it in the state fallback-git too (s3p2-oldgit section 5, Linux VM).
//
// A request without the member in the state shadow has the other form
// (settingsGateRefusal sets it). Its calls name the folder of the repository in
// the folder of the daemon, so git reads the daemon's copy of the settings.
type gitUse struct {
	// host is the settings control of the daemon. folder is the folder of the
	// daemon, <S>, as it was when the request passed its gate.
	host   *shadowHost
	folder string
	// gitDir is the git directory of the repository whose copy this request
	// brought up to date. repoDir is the folder of that copy, <S>/<32 hex>.
	gitDir  string
	repoDir string
	// atReply takes the removal of a short-lived folder that stays until the
	// reply of the request (request.runAtReply), or it is nil.
	atReply *[]func()
	// items are the `-c` items of the `config.worktree` file of the repository
	// (worktreeConfigItems). entryItems are those of the file of the linked
	// worktree of a git.info. Both are nil with no such file.
	items      []string
	entryItems []string
	// shared is the `core.sharedRepository` setting of the config of the
	// repository (makeWorktreesFolder).
	shared sharedRepository
	// accepted is the held git directory of a create, where gitDirMustBeReal is
	// set (acceptedGitDir). It is nil in every other request.
	accepted *acceptedGitDir
	// lightSix gives the light calls of a request with no folder the six values
	// of profileEnv (noRepoCreateCalls).
	lightSix bool
	// branchGate is the refusal of the config of the repository before the
	// branch step of a rollback, or nil for no such read (runBranchStep).
	branchGate func() string
}

// inFolder reports whether the request keeps its short-lived folders in the
// folder of the daemon, and sends six profile values on its light calls. It
// does in the state shadow, and in the state fallback-git without the member.
//
// In the state fallback-git 5fd08069 names `<S>/.gitdir-<n>` in a git.status
// and `<S>/.index-<n>` in the read-tree of a create. Its light calls of a
// create and the listings of create and remove carry
// GIT_ALLOW_PROTOCOL=denied_by_claude_ssh, GIT_ASKPASS= and GIT_NO_LAZY_FETCH=1
// there. The cells are Q-S4c of val-s3p2b2-3-windows (Windows VM),
// S4-wt1-first-status of val-s3p2b2-1-macos (macOS VM) and c29-first-wt-fails
// of val-s3p2b2-1-linux for the git.status folder (Linux VM). The macOS and
// Windows call logs sort the variables by name, so the order of the six values
// in that state is not measured: it is the order of the state shadow. The
// folder of the include step in that state is not measured: it is in the
// folder of the daemon too.
func (u gitUse) inFolder() bool {
	return u.folder != ""
}

// heldGitDir is the path of the git directory that the request acts in: the
// held folder of a create, or gitDir. The two differ only after a create moved
// its hold (acceptedGitDir.movedTo).
func (u gitUse) heldGitDir() string {
	if u.accepted != nil {
		return u.accepted.path
	}
	return u.gitDir
}

// shadow reports whether the calls of the request name the copy.
func (u gitUse) shadow() bool {
	return u.repoDir != ""
}

// worktreeAdd runs one `git worktree add` of git.worktree_create in repo, with
// its listing before it. It returns the stderr of the call, untrimmed, and the
// exec error. stdout is not kept, because the failure frames of
// git.worktree_create quote stderr only (measured against f6010b97 and
// 90fca6e6 on a macOS VM, see worktreeGitText). leaf is the folder of the new
// worktree, and cp is the checkpoint of the create, which holds that folder
// open.
//
// In the state shadow on Linux and macOS the call and its listing carry
// GIT_COMMON_DIR=<copy>, as on 5fd08069 (val-s3p2b1-linux cell A3-create-plain,
// val-s3p2b1-macos row N1). Git then writes the registration through the link
// `worktrees` of the copy. Two steps of 5fd08069 go with that (cells N1 and N2
// of val-s3p2b2-3-linux and val-s3p2b2-3-macos):
//
//   - After the listing and before the add, the daemon makes the folder
//     `worktrees` of the git directory when it is not there (makeWorktreesFolder).
//   - After the add ended, the daemon looks at the `.git` file of the new
//     worktree (repointLeaf). claustrum does that after its tests of the new
//     registration and before its next git call.
//
// On Windows the copy has no link `worktrees`. There the call gets a folder of
// its own in the state shadow, and other steps follow it (worktreeAddStaged).
// With the member and in the state fallback-git the call names no copy, on
// every system: on a Windows VM the add of 5fd08069 carries
// GIT_COMMON_DIR=<git directory> in the state fallback-git, and it names no
// `.s<n>` folder (val-s3p2b2-3-windows cell Q-S4c).
func worktreeAdd(ctx context.Context, u gitUse, repo, leaf string, cp worktreeCheckpoint, args ...string) (string, error) {
	_, named := u.names(repo)
	if named && shadowStagesAdd {
		stderr, err := worktreeAddStaged(ctx, u, repo, leaf, cp, args...)
		if err == nil && u.accepted != nil {
			u.accepted.moved = true
		}
		return stderr, err
	}
	named = named && shadowLinksWorktrees
	cmd := hardenedGitCmd(ctx, u, repo, false, nil, args...)
	// A create that holds its git directory runs no `worktree add` when the
	// path no longer names the held folder. The test comes after the listing.
	if u.swapped("plain add") {
		// The text has the name of the git directory as the request spells it.
		gitDir := u.gitDir
		if u.accepted != nil && u.accepted.sent != "" {
			gitDir = u.accepted.sent
		}
		return "", errors.New(plainAddRefusal(gitDir, named))
	}
	if named {
		if text := u.worktreesFolderRefusal(); text != "" {
			return "", &addNotPrepared{text: text}
		}
		u.makeWorktreesFolder()
		if text := u.worktreesOpenRefusal(); text != "" {
			return "", &addNotPrepared{text: text}
		}
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil && u.accepted != nil {
		u.accepted.moved = true
	}
	return errBuf.String(), err
}

// plainAddRefusal is the error of a `worktree add` that does not run, because
// the path of the git directory gitDir no longer names the folder that the
// create holds. named says whether the call names the copy of the request.
//
// On a macOS VM `.git` becomes a symbolic link after `rev-parse
// --absolute-git-dir`. 5fd08069 then makes the listing of the add and no
// `worktree add` (val-s3p2b2-9-macos, macOS VM):
//
//   - The link leads to a copy of the git directory: `git worktree add failed:
//     cannot prepare the repository's worktrees directory: ".git" is a link or
//     a file where Claude needs a real directory; put the directory itself in
//     its place` (cells swap-copy and swap-copyold, 7 of 7 runs).
//   - The link leads to a folder with one marker file: `git worktree add
//     failed: cannot list the repository's worktree entries: ".git" is a link
//     …`, with the same end (cells swap-marker, 4 of 4 runs). The listing
//     carries GIT_DIR=/dev/null there.
//
// The Linux VM has the same two texts (val-s3p2b2-9-linux2, 2 of 2 runs for
// each form). On Windows the call comes
// here only when it does not name the copy. The text is that of a junction
// that was put at `.git` after the listing of the add (cells X1c-09 and
// X1p-09 of val-s3p2b2-7-windows, Windows VM).
func plainAddRefusal(gitDir string, named bool) string {
	switch {
	case shadowStagesAdd:
		return prepareWorktreesPrefix + repoConfigPrefix + gitDirLinkText(gitDir)
	case named:
		return prepareWorktreesPrefix + realDirText(gitDir)
	}
	return listEntriesPrefix + realDirText(gitDir)
}

// worktreesFolderRefusal is the error of a `worktree add` that does not run,
// because the entry `worktrees` of the git directory is there and is no real
// folder. It is "" in every other case. With a symbolic link to a folder there,
// 5fd08069 answers `git worktree add failed: cannot prepare the repository's
// worktrees directory: "<git directory>/worktrees" is not a directory`. It runs
// no `worktree add`, and no leaf and no branch stay (cell V1 of round tier0-10,
// Linux VM, 3 of 3 runs). The test is where the daemon makes that folder, so it
// runs in the state shadow on Linux and macOS only. The other states are not
// measured. In attach mode 5fd08069 answers the same text one time (cell E1 of
// round tier0-11, Linux VM): the caller makes no second add (addNotPrepared).
//
// A file at the name gets the same text (cell C4 of round tier0-24, Linux
// VM). The entry is read through the held git directory, or by the path for a
// request that holds none. claustrum's own, not measured: the text names the
// held git directory, with no cut of a long path.
func (u gitUse) worktreesFolderRefusal() string {
	var fi os.FileInfo
	var err error
	if u.accepted != nil {
		fi, err = u.accepted.root.Lstat(worktreesSubdir)
	} else {
		fi, err = os.Lstat(filepath.Join(u.gitDir, worktreesSubdir))
	}
	if err != nil || fi.IsDir() {
		return ""
	}
	return prepareWorktreesPrefix + fmt.Sprintf("%q is not a directory", filepath.Join(u.heldGitDir(), worktreesSubdir))
}

// worktreesOpenRefusal is the error of a `worktree add` that does not run,
// because the folder `worktrees` of the git directory is there and cannot be
// opened. It is "" in every other case. With that folder at mode 0300, 5fd08069
// answers `git worktree add failed: cannot prepare the repository's worktrees
// directory: openat worktrees: permission denied`. It runs no `worktree add`,
// and no leaf, branch or registration stays (cell C5 of round tier0-24, Linux
// VM, and round tier0-25m, macOS VM). Only mode 0300 is measured. claustrum
// opens the folder through a root at the git directory, which gives that
// text. The open runs after makeWorktreesFolder. A folder that does not exist
// then keeps the earlier rule: the add runs (not measured).
func (u gitUse) worktreesOpenRefusal() string {
	var root *os.Root
	if u.accepted != nil {
		root = u.accepted.root
	} else {
		r, err := os.OpenRoot(u.gitDir)
		if err != nil {
			return ""
		}
		defer func() { _ = r.Close() }()
		root = r
	}
	dir, err := root.OpenRoot(worktreesSubdir)
	if err == nil {
		_ = dir.Close()
		return ""
	}
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return prepareWorktreesPrefix + err.Error()
}

// offWorktreesRefusal is the refusal of a create with "shadowDirOff":true in
// repo whose registrations folder is there and is no real folder, or "". With a
// symbolic link to a folder there, 5fd08069 answers `git worktree add failed:
// cannot list the repository's worktree entries: "worktrees" is a link or a
// file where Claude needs a real directory; put the directory itself in its
// place`. It runs no `worktree add`, makes nothing behind the link and leaves
// no branch, and R/.claude/worktrees stays (cell C2 of round tier0-24, Linux
// VM, and of round tier0-25m, macOS VM). The folder is that of the git
// directory that the trust check pins. claustrum's own, not measured: a file
// at the name, and a daemon GIT_COMMON_DIR, with which git writes elsewhere.
// Windows has no such test.
func offWorktreesRefusal(repo string) string {
	if !shadowLinksWorktrees {
		return ""
	}
	t := gitDirTrustFor(repo)
	if t.verdict != gitDirTrusted || t.pinCommonDir == "" {
		return ""
	}
	dir := filepath.Join(t.pinCommonDir, worktreesSubdir)
	if fi, err := os.Lstat(dir); err != nil || fi.IsDir() {
		return ""
	}
	return listEntriesPrefix + realDirText(dir)
}

// makeWorktreesFolder makes the folder `worktrees` in the git directory gitDir
// when there is none. On 5fd08069 that folder is there, empty, before the
// `worktree add` call starts, and no git process runs when it appears. It
// stays after an add that fails. 89cb6289 makes no such folder: there git makes
// it (cells N2 of val-s3p2b2-3-linux, 9 of 9 and 6 of 6 runs, Linux VM, and of
// val-s3p2b2-3-macos, macOS VM).
//
// The folder is made through a root at gitDir with the fixed name `worktrees`.
// gitDir is the git directory that the trust check pinned. A create that holds
// its git directory uses the held folder as that root (acceptedGitDir), and
// opens nothing by the path. A folder that cannot be made is not measured: the
// add runs and gives its own error.
//
// The mkdir has mode 0777, and the umask of the daemon takes its bits from
// that. With no `core.sharedrepository` line in the copy and with the line
// `umask` the folder keeps that mode: `d 0755` with umask 022 and `d 0775`
// with 002. With another line the folder that this call made gets one mode call
// (sharedRepository.folderMode). A trace of 5fd08069 has `mkdirat(15,
// "worktrees", 0777)` and then the mode call (the 18 rows of group P5 of
// val-s3p2b2-6-linux, Linux VM). The registration that git makes in the folder
// takes the set-group-ID bit from it: `d 2755` with umask 022.
//
// Not measured: a folder `worktrees` that is there before the create, macOS and
// Windows. A folder that is there gets no mode call.
func (u gitUse) makeWorktreesFolder() {
	if u.accepted != nil {
		makeWorktreesFolderIn(u.accepted.root, u.shared)
		return
	}
	root, err := os.OpenRoot(u.gitDir)
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	makeWorktreesFolderIn(root, u.shared)
}

// makeWorktreesFolderIn is makeWorktreesFolder through root, the root at the
// git directory.
func makeWorktreesFolderIn(root *os.Root, shared sharedRepository) {
	if err := root.Mkdir(worktreesSubdir, 0o777); err != nil {
		return
	}
	fi, err := root.Lstat(worktreesSubdir)
	if err != nil || !fi.IsDir() {
		return
	}
	if mode, ok := shared.folderMode(fi.Mode()); ok {
		_ = root.Chmod(worktreesSubdir, mode)
	}
}

// repointLeaf is the step of a create in repo that looks at the `.git` file of
// its new worktree leaf (repointWorktreeGitFile). It acts only in the state
// shadow on Linux and macOS, where the copy has the link `worktrees`. A file
// that does not name the copy stays as it is (repointWorktreeGitFile).
//
// The create runs it after its tests of the new registration. After a refusal
// of those tests the file still names the copy on 5fd08069: `gitdir:
// <copy>/worktrees/w1` in cells V2, V5c, V6 and V7 of round tier0-10 (Linux VM,
// 1 run each, V7 3 runs). The same holds after the refusal of a git directory that
// is no longer the held folder (cell swb-wtadd of val-s3p2b2-13-linux2, Linux
// VM, 2 runs). Not measured: the order of this step and the test of the leaf
// identity (cell V8 has one result for both orders).
func (u gitUse) repointLeaf(repo, leaf string, cp worktreeCheckpoint) {
	if u.shadow() && shadowLinksWorktrees {
		repointWorktreeGitFile(u, leaf, cp)
	}
}

// repointWorktreeGitFile replaces the `.git` file of the new worktree leaf when
// its text names the registration through the folder of the copy.
//
// On a Linux VM with git 2.43.0 the add of 5fd08069 writes `gitdir:
// <copy>/worktrees/<name>` and a newline into that file. 4 to 25 ms after the
// call ended, and before the next call, the file is a new file with the text
// `gitdir: <git directory>/worktrees/<name>` and a newline, in 6 of 6 runs
// (cell N1 of val-s3p2b2-3-linux). On a macOS VM with git 2.50.1 git writes
// the path in the repository itself, and the file keeps its text and its inode
// (cell N1 of val-s3p2b2-3-macos).
//
// The file is replaced only after the daemon read it and its text is that one
// line: `gitdir: `, the folder of the copy, `/worktrees/`, one name with no
// separator, and a newline. Every other text is left as it is
// (replaceWorktreeGitFile). The new file is made with mode 0666, and the umask
// of the daemon takes its bits from that. On 5fd08069 the file is `f 0664`
// with a daemon umask of 002, in 13 of 13 creates (cells p2u-12a, p2u-12d and
// p2u-12f and 10 creates of group P5 with that umask, val-s3p2b2-6-linux,
// Linux VM). With umask 022 it is `f 0644`, and with umask 000 it is `f 0666`
// (cell N-d of val-s3p2b2-8-linux2, Linux VM, 1 run for each umask).
func repointWorktreeGitFile(u gitUse, leaf string, cp worktreeCheckpoint) {
	replaceWorktreeGitFile(leaf, cp, func(old string) (string, bool) {
		prefix := "gitdir: " + filepath.Join(u.repoDir, worktreesSubdir) + string(filepath.Separator)
		name, ok := strings.CutPrefix(old, prefix)
		if !ok {
			return "", false
		}
		name, ok = strings.CutSuffix(name, "\n")
		if !ok || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\r\n") {
			return "", false
		}
		return "gitdir: " + filepath.Join(u.heldGitDir(), worktreesSubdir, name) + "\n", true
	})
}

// replaceWorktreeGitFile replaces the `.git` file of the new worktree leaf with
// the text that next gives for its old text. It reports whether the new file
// was written. Nothing is changed when next refuses the old text.
//
// The remove and the new file go through a root at leaf, with the fixed name
// `.git`. leaf must still be the folder that the create made and holds open
// (cp), and `.git` must be a regular file of at most repointMaxBytes.
func replaceWorktreeGitFile(leaf string, cp worktreeCheckpoint, next func(old string) (string, bool)) bool {
	if cp.info == nil {
		return false
	}
	root, err := os.OpenRoot(leaf)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	if fi, err := root.Stat("."); err != nil || !os.SameFile(fi, cp.info) {
		return false
	}
	if fi, err := root.Lstat(".git"); err != nil || !fi.Mode().IsRegular() || fi.Size() > repointMaxBytes {
		return false
	}
	old, err := root.ReadFile(".git")
	if err != nil {
		return false
	}
	text, ok := next(string(old))
	if !ok {
		return false
	}
	if err := root.Remove(".git"); err != nil {
		return false
	}
	f, err := root.OpenFile(".git", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return false
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err == nil
}

// repointMaxBytes bounds the read of the `.git` file of a new worktree
// (repointWorktreeGitFile). The value is claustrum's own.
const repointMaxBytes = 8192

// names reports whether a call in dir names the copy of this request. It does
// when the request has a copy, and the trust check pins dir to the git
// directory of that copy. A call in any other folder keeps the form of commonDirPinEnv. With a
// GIT_COMMON_DIR and no GIT_DIR in the daemon's own environment no call names
// the copy (daemonCommonDirStays).
func (u gitUse) names(dir string) (gitDirTrust, bool) {
	if !u.shadow() || daemonCommonDirStays() {
		return gitDirTrust{}, false
	}
	t := gitDirTrustFor(dir)
	// A create that holds its git directory keeps the verdict of its gate for
	// its baseRepo, as long as the trust check finds a git directory there.
	// After a symbolic link to a copy of the git directory was put at `.git`,
	// the next listing of 5fd08069 carries GIT_COMMON_DIR=<copy of the
	// request>, GIT_DIR=<repo>/.git and GIT_WORK_TREE=<repo>, as before the
	// swap (cells swap-copy of val-s3p2b2-9-macos, macOS VM). With a link to a
	// folder that is no git directory it carries GIT_DIR=/dev/null (cells
	// swap-marker).
	if a := u.accepted; a != nil && a.dir != "" && dir == a.dir && t.verdict == gitDirTrusted && t.pinCommonDir != "" {
		return a.trust, true
	}
	return t, t.verdict == gitDirTrusted && t.pinCommonDir == u.gitDir
}

// noWorkTree reports whether dir is a repository with no work tree whose calls
// name the copy. That is a bare repository, or a folder inside a git directory.
//
// For a bare `baseRepo` the `rev-parse --is-inside-work-tree` call of a create
// prints `true` in the state shadow. The call carries GIT_DIR and the copy, and
// no GIT_WORK_TREE. 5fd08069 answers "not a git repository" with `not_a_repo`
// after that call, its call 15, and makes nothing (cell x2-bare-create of
// val-s3p2b2-5-linux2, Linux VM, 3 of 3 runs). With the member claustrum
// answers the same line, because git prints `false` there. 5fd08069 is not
// measured with the member.
//
// Which test of 5fd08069 refuses is not measured. It is not the `core.bare`
// value of the copy. Cell 9a has `bare = true` in the `config.worktree` file of
// a repository with a work tree. The copy then holds `bare = "true"` as its
// last `core` block, and the create of 5fd08069 succeeds (s3p2b-edges2-linux
// and val-s3p2b2-5-linux, Linux VM). claustrum tests what the walk of the
// trust check found: a git directory that no folder holds as its `.git` entry
// (gitWalkRoot). With a GIT_DIR in the daemon's own environment the walk does
// not say that, and the test does not run (not measured).
func (u gitUse) noWorkTree(dir string) bool {
	if _, ok := u.names(dir); !ok || os.Getenv("GIT_DIR") != "" {
		return false
	}
	root, _ := gitWalkRoot(dir)
	return root == ""
}

// commonVars are the two folder variables that every call on the repository
// carries in the state shadow: GIT_COMMON_DIR=<copy> and
// GIT_OBJECT_DIRECTORY=<git directory>/objects. The call logs of 5fd08069 show
// them on a Linux VM (s3-linux cell B0, s3p2-linux cell B3a). They show them
// on a macOS VM (s3p2-macos cell B3b) and on a Windows VM (s3p2-windows cell
// B3a) too. A bare
// repository has GIT_OBJECT_DIRECTORY=<repo>/objects (s3p2-linux cell B3c).
func (u gitUse) commonVars() []string {
	return []string{"GIT_COMMON_DIR=" + u.repoDir, "GIT_OBJECT_DIRECTORY=" + filepath.Join(u.gitDir, "objects")}
}

// pin is the folder variables of a git call in dir with no --git-dir= in its
// argv. In the state shadow they are GIT_COMMON_DIR, GIT_OBJECT_DIRECTORY,
// GIT_DIR and GIT_WORK_TREE, in this order. Cells A1, A3 and A4 of
// val-s3p2b1-linux show the order on a Linux VM, and section 2.6 of
// val-s3p2b1-macos on a macOS VM. Windows has no record of the order.
//
//   - GIT_DIR is the git directory of dir. For a linked worktree it is the entry
//     under `worktrees`. The two first variables are then those of the
//     repository (s3p2-linux cell B3b, s3p2-macos cell B3b).
//   - GIT_WORK_TREE is the folder that holds the `.git` entry. A folder inside
//     the work tree gets the root of the work tree (s3p2-linux2 cell D7b, Linux
//     VM). A bare repository has no GIT_WORK_TREE (s3p2-linux cell B3c).
//
// With a GIT_DIR in the daemon's own environment, the trust check judges that
// directory, and the walk from dir does not find it. The call then gets the two
// first variables only. The daemon's own GIT_DIR stays at its place in the
// environment, before the profile values, and the call has no GIT_WORK_TREE.
// On 5fd08069 each such call has GIT_DIR=<the daemon's value>, the profile
// values, GIT_COMMON_DIR=<copy> and GIT_OBJECT_DIRECTORY=<that git
// directory>/objects, in this order (cell o1-gd-v2 of val-s3p2b2-7-linux2,
// Linux VM, 27 calls). A relative GIT_DIR of the daemon is not measured.
func (u gitUse) pin(dir string) []string {
	t, ok := u.names(dir)
	if !ok {
		return commonDirPinEnv(dir)
	}
	gitDir := t.pinCommonDir
	if t.worktreeEntry != "" {
		gitDir = t.worktreeEntry
	}
	if os.Getenv("GIT_DIR") != "" {
		return u.commonVars()
	}
	env := append(u.commonVars(), "GIT_DIR="+gitDir)
	if root, _ := gitWalkRoot(dir); root != "" {
		env = append(env, "GIT_WORK_TREE="+root)
	}
	return env
}

// gitDirPin is the folder variables of a git call with --git-dir= in its argv,
// for the repository of dir. In the state shadow they are GIT_COMMON_DIR and
// GIT_OBJECT_DIRECTORY only. Such a call has no GIT_DIR and no GIT_WORK_TREE.
// Cells A1 and A3 of val-s3p2b1-linux show that on a Linux VM, and cell 50 of
// s3p2b-edges on macOS and Windows VMs.
func (u gitUse) gitDirPin(dir string) []string {
	if _, ok := u.names(dir); !ok {
		return commonDirPinEnv(dir)
	}
	// After a create moved its hold, a call with --git-dir= names the objects
	// of that folder (cell swb-first of val-s3p2b2-10-linux2, Linux VM).
	return []string{"GIT_COMMON_DIR=" + u.repoDir, "GIT_OBJECT_DIRECTORY=" + filepath.Join(u.heldGitDir(), "objects")}
}

// profileEnv is profileEnv for a call of this request. In the state shadow a
// light call carries the six values of the heavy profile, in another order.
// 5fd08069 sends them so on the light calls of a create outside the new
// worktree, and on the listing before each. It sends them on the three
// listings of the branch step too. Rows N1 and N2 of val-s3p2b1-linux have the
// order of the Linux call log. Row N1 of val-s3p2b1-macos has the same order.
// Row N1 of val-s3p2b1-windows has the six values and no order. With the
// member those calls carry the four values of the light profile.
//
// The listings and the read-tree in the new worktree keep the four values in
// both forms (rows N3 and N4). They do not come here.
func (u gitUse) profileEnv(heavy bool) []string {
	if heavy || (!u.inFolder() && !u.lightSix) {
		return profileEnv(heavy)
	}
	return []string{
		"GIT_ALLOW_PROTOCOL=denied_by_claude_ssh",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_GRAFT_FILE=" + os.DevNull,
		"GIT_NO_LAZY_FETCH=1",
		"GIT_ASKPASS=",
	}
}

// precursorEnv is precursorEnv with the profile of this request.
func (u gitUse) precursorEnv(heavy bool, pin []string) []string {
	env := append(listingEnvBase(), u.profileEnv(heavy)...)
	env = append(env, pin...)
	return append(env, "LC_ALL=C", "LANGUAGE=C")
}

// hardenedGitEnv is hardenedGitEnv with the profile of this request.
func (u gitUse) hardenedGitEnv(heavy bool, pin, hooks []string) []string {
	return hardenedEnvWith(gitChildEnv(), u.profileEnv(heavy), pin, hooks)
}

// The names of the short-lived folders of one request. In the state shadow
// they are in the folder of the daemon, with a decimal number after the name.
// 5fd08069 has three of them:
//
//   - `.gitdir-<n>` for one git.status (s3p2-linux cell B3a, Linux VM, and cell
//     22 of s3p2b-edges, Windows VM).
//   - `.index-<n>` for the checkout of one create (val-s3p2b1-linux cell
//     A3-create-plain). It has mode 0700 on a Linux VM (val-s3p2b2-1-linux cell
//     s3-first-create).
//   - `.worktreeinclude-<n>` with the file `patterns` for one include step
//     (val-s3p2b1-linux row N11).
//
// With the member the folders are in the temporary folder. 5fd08069 has
// `claude-ssh-gitdir-<n>`, `claude-ssh-index-<n>` and
// `claude-ssh-worktreeinclude-<n>` there. The last one holds the file
// `patterns` (val-s3p2b1-linux section 1.2, rows D5 and D6).
const (
	shadowStatusDirPrefix  = ".gitdir-"
	shadowIndexDirPrefix   = ".index-"
	shadowIncludeDirPrefix = ".worktreeinclude-"
	includePatternsName    = "patterns"
)

// requestDir makes a new folder for one request and returns its path and the
// function that removes it. In the state shadow the folder is
// <S>/<shadowPrefix><n>, made with mode 0700 through a root at the folder of
// the daemon, and removed through removeEntry. In every other state it is a
// new folder with tempPrefix in the temporary folder, as before.
//
// The shutdown of the daemon removes its folder with every such entry in it,
// also while the request runs (close). 5fd08069 does so with a `.gitdir-<n>`
// in flight (s3p2-linux2 cell F3, Linux VM).
//
// The error of a folder that cannot be made in the folder of the daemon names
// its full path, with the operation `mkdir`. With a folder of the daemon of
// mode 0500 a create of 5fd08069 answers `git worktree add failed (checkout):
// mkdir <S>/.index-<n>: permission denied` (cell f1-0500 of
// val-s3p2b2-7-linux2, Linux VM). On a Windows VM the folder `.s<n>` of a
// create gives `mkdir C:\o\w\run\git-shadow\<S>\.s<n>: Access is denied.`
// (tables W3 and F1 of val-s3p2b2-7-windows). A folder of the daemon that
// cannot be opened is not measured: it gives the error of that open.
func (u gitUse) requestDir(shadowPrefix, tempPrefix string) (dir string, remove func(), err error) {
	if !u.inFolder() {
		dir, err = os.MkdirTemp("", tempPrefix)
		if err != nil {
			return "", func() {}, err
		}
		return dir, func() { _ = os.RemoveAll(dir) }, nil
	}
	root, err := os.OpenRoot(u.folder)
	if err != nil {
		return "", func() {}, err
	}
	defer func() { _ = root.Close() }()
	for range 10000 {
		var n [4]byte
		if _, err = rand.Read(n[:]); err != nil {
			return "", func() {}, err
		}
		name := shadowPrefix + strconv.FormatUint(uint64(binary.LittleEndian.Uint32(n[:])), 10)
		err = root.Mkdir(name, 0o700)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			var pe *os.PathError
			if errors.As(err, &pe) {
				err = &os.PathError{Op: "mkdir", Path: filepath.Join(u.folder, name), Err: pe.Err}
			}
			return "", func() {}, err
		}
		rel := filepath.Join(filepath.Base(u.folder), name)
		return filepath.Join(u.folder, name), func() { _ = u.host.removeEntry(rel) }, nil
	}
	return "", func() {}, err
}
