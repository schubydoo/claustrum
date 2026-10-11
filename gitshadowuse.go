package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
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
	// ownConfig is true for the calls that keep GIT_COMMON_DIR=<gitDir> in the
	// state shadow (ownConfigCalls).
	ownConfig bool
	// atReply takes the removal of a short-lived folder that stays until the
	// reply of the request (request.runAtReply), or it is nil.
	atReply *[]func()
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

// shadow reports whether the calls of the request name the copy.
func (u gitUse) shadow() bool {
	return u.repoDir != ""
}

// ownConfigCalls is u for the `worktree add` calls of git.worktree_create and
// the listing before each on Windows. Those calls keep GIT_COMMON_DIR=<git
// directory> there in this build, with the other three folder variables of
// 5fd08069. On a Windows VM the add of 5fd08069 carries the folder `.s<n>`
// (cell 50 of s3p2b-edges), which this build does not make. So the add reads
// the repository's own config there, as it did before, and the listing that
// gives its hook pins reads it too.
func (u gitUse) ownConfigCalls() gitUse {
	u.ownConfig = true
	return u
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
//   - After the add ended and before the next call, the daemon looks at the
//     `.git` file of the new worktree (repointWorktreeGitFile).
//
// On Windows neither step runs (ownConfigCalls).
func worktreeAdd(ctx context.Context, u gitUse, repo, leaf string, cp worktreeCheckpoint, args ...string) (string, error) {
	_, named := u.names(repo)
	named = named && shadowLinksWorktrees
	addUse := u
	if !shadowLinksWorktrees {
		addUse = u.ownConfigCalls()
	}
	cmd := hardenedGitCmd(ctx, addUse, repo, false, nil, args...)
	if named {
		makeWorktreesFolder(u.gitDir)
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if named && err == nil {
		repointWorktreeGitFile(u, leaf, cp)
	}
	return errBuf.String(), err
}

// makeWorktreesFolder makes the folder `worktrees` in the git directory gitDir
// when there is none. On 5fd08069 that folder is there, empty and with mode
// 0755, before the `worktree add` call starts, and no git process runs when it
// appears. It stays after an add that fails. 89cb6289 makes no such folder:
// there git makes it (cells N2 of val-s3p2b2-3-linux, 9 of 9 and 6 of 6 runs,
// Linux VM, and of val-s3p2b2-3-macos, macOS VM).
//
// The folder is made through a root at gitDir with the fixed name `worktrees`.
// gitDir is the git directory that the trust check pinned. A folder that
// cannot be made is not measured: the add runs and gives its own error.
func makeWorktreesFolder(gitDir string) {
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	_ = root.Mkdir(worktreesSubdir, 0o755)
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
// separator, and a newline. Every other text is left as it is. The remove and
// the new file go through a root at leaf, with the fixed name `.git`, and leaf
// must still be the folder that the create made and holds open (cp). The mode
// of the new file is not measured: it is 0644, the mode of the file of git.
func repointWorktreeGitFile(u gitUse, leaf string, cp worktreeCheckpoint) {
	if cp.info == nil {
		return
	}
	root, err := os.OpenRoot(leaf)
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	if fi, err := root.Stat("."); err != nil || !os.SameFile(fi, cp.info) {
		return
	}
	if fi, err := root.Lstat(".git"); err != nil || !fi.Mode().IsRegular() || fi.Size() > repointMaxBytes {
		return
	}
	old, err := root.ReadFile(".git")
	if err != nil {
		return
	}
	prefix := "gitdir: " + filepath.Join(u.repoDir, worktreesSubdir) + string(filepath.Separator)
	name, ok := strings.CutPrefix(string(old), prefix)
	if !ok {
		return
	}
	name, ok = strings.CutSuffix(name, "\n")
	if !ok || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\r\n") {
		return
	}
	if err := root.Remove(".git"); err != nil {
		return
	}
	f, err := root.OpenFile(".git", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString("gitdir: " + filepath.Join(u.gitDir, worktreesSubdir, name) + "\n")
	_ = f.Close()
}

// repointMaxBytes bounds the read of the `.git` file of a new worktree
// (repointWorktreeGitFile). The value is claustrum's own.
const repointMaxBytes = 8192

// names reports whether a call in dir names the copy of this request. It does
// when the request has a copy, and the trust check pins dir to the git
// directory of that copy. A call in any other folder keeps the form of commonDirPinEnv. With a
// GIT_COMMON_DIR in the daemon's own environment no call names the copy (not
// measured).
func (u gitUse) names(dir string) (gitDirTrust, bool) {
	if !u.shadow() || daemonCommonDirSet() {
		return gitDirTrust{}, false
	}
	t := gitDirTrustFor(dir)
	return t, t.verdict == gitDirTrusted && t.pinCommonDir == u.gitDir
}

// commonVars are the two folder variables that every call on the repository
// carries in the state shadow: GIT_COMMON_DIR=<copy> and
// GIT_OBJECT_DIRECTORY=<git directory>/objects. The call logs of 5fd08069 show
// them on a Linux VM (s3-linux cell B0, s3p2-linux cell B3a). They show them
// on a macOS VM (s3p2-macos cell B3b) and on a Windows VM (s3p2-windows cell
// B3a) too. A bare
// repository has GIT_OBJECT_DIRECTORY=<repo>/objects (s3p2-linux cell B3c).
func (u gitUse) commonVars() []string {
	common := u.repoDir
	if u.ownConfig {
		common = u.gitDir
	}
	return []string{"GIT_COMMON_DIR=" + common, "GIT_OBJECT_DIRECTORY=" + filepath.Join(u.gitDir, "objects")}
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
// directory, and the walk from dir does not find it. The call then gets the
// judged directory as GIT_DIR and no GIT_WORK_TREE of its own (not measured).
func (u gitUse) pin(dir string) []string {
	t, ok := u.names(dir)
	if !ok {
		return commonDirPinEnv(dir)
	}
	gitDir := t.pinCommonDir
	if t.worktreeEntry != "" {
		gitDir = t.worktreeEntry
	}
	env := append(u.commonVars(), "GIT_DIR="+gitDir)
	if os.Getenv("GIT_DIR") != "" {
		return env
	}
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
	return u.commonVars()
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
	if heavy || !u.inFolder() {
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
	return hardenedEnvWith(os.Environ(), u.profileEnv(heavy), pin, hooks)
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
			return "", func() {}, err
		}
		rel := filepath.Join(filepath.Base(u.folder), name)
		return filepath.Join(u.folder, name), func() { _ = u.host.removeEntry(rel) }, nil
	}
	return "", func() {}, err
}
