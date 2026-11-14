package git

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

	"github.com/schubydoo/claustrum/internal/homeguard"
)

// gitUse says how the git calls of one request name their repository.
//
// The zero value is the form that claustrum had before: a call carries
// GIT_COMMON_DIR=<git directory> and reads the repository's own config. It is
// the form of a request with "shadowDirOff":true, of the state fallback-git and
// of a server with no run folder. 5fd08069 sends that one folder variable with
// the member (measured on Linux, macOS and Windows). It sends it in the state
// fallback-git too (measured on Linux).
//
// A request without the member in the state shadow has the other form
// (settingsGateRefusal sets it). Its calls name the folder of the repository in
// the folder of the daemon, so git reads the daemon's copy of the settings.
type gitUse struct {
	// host is the settings control of the daemon. folder is the folder of the
	// daemon, <S>, as it was when the request passed its gate.
	host   *ShadowHost
	folder string
	// gitDir is the git directory of the repository whose copy this request
	// brought up to date. repoDir is the folder of that copy, <S>/<32 hex>.
	gitDir  string
	repoDir string
	// read is the read of the config whose text is in the copy. The build of a
	// git call makes a folder that went again from it (renewCopy). noRenew
	// skips that step for the env of a call whose listing was its build.
	read    *repoVerdict
	noRenew bool
	// noRecheck skips recheck before a call. The calls after the checkout of a
	// create set it (worktreecontent.go).
	noRecheck bool
	// atReply takes the removal of a short-lived folder that stays until the
	// reply of the request (gitCall.runAtReply), or it is nil.
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
	// stop holds the time limits of the git calls of the request (gitstop.go), or
	// is nil.
	stop *gitStop
	// live is the off switch of a git request after its gate (atCommand), or
	// nil.
	live *liveSwitch
	// sshKept is set in a git.worktree_create and a git.worktree_remove. It
	// reports whether the off switch holds the daemon now (gitCall.offLive).
	// It is nil in every other request (gitUse.sshOff).
	sshKept func() bool
}

// liveSwitch lets the later git commands of a request follow the off switch.
// On 5fd08069 the hold changes the form of the git commands of a git.info
// while it runs (measured on Linux):
//
//   - In a measurement, the first two repository commands after the start
//     test still name the copy, and the later commands name <repo>/.git.
//   - In a measurement, the hold ends while the request runs. A later
//     command runs the start test, and the commands after it name the copy.
//
// claustrum takes the form at each command that runs its own config listing
// (hardenedGitCmd). The config check and the call after it keep the form of the
// gate, and so does listEntryConfig. The Windows root pair of gitInfoRoot takes
// the form of its moment (measured on Windows). git.list_branches follows the
// switch too (measured on Linux).
//
// git.status, git.worktree_create and git.worktree_remove follow it in the same
// way (measured on Linux). Their later gate asks git about the repository before
// its read of the config when the command that runs it is a call in the
// repository with no --git-dir= (measured). After a later gate at a call with
// --git-dir= no such call follows (measured). claustrum tells the two by the
// kind of the call (formNow): the cells measure the outcome, not the cause.
//
// A create adopts its later gate until its first good `worktree add`
// (keepLiveCreate). It then takes the held git directory as a create whose
// first gate had the copy does, and every later step acts through it. A
// later gate after that add changes only the form of the git commands, and so
// does each later gate of a status and a remove (keepLiveForm).
type liveSwitch struct {
	// off reports whether the off switch holds the daemon now.
	off func() bool
	// gate runs the gate of the request again and returns the form that it
	// gives. ok is false after a refusal. ask is true for a command in the
	// repository with no --git-dir=. pending is true while the gate of the
	// request ran under the hold and gate has not run since.
	gate    func(ask bool) (use gitUse, ok bool)
	pending bool
	// regated is the form that gate gave, or nil before a gate that passed. A
	// helper that has its use by value gets it at its next command. adopted is
	// true when the request took that form as its own use (keepLiveCreate):
	// a use with a copy is then the current one.
	regated *gitUse
	adopted bool
	// stop holds the time limits of the git calls of the request.
	stop *gitStop
}

// atCommand is the form of a git command in the repository that starts now
// (liveSwitch). A gate that a later command runs and that refuses leaves the
// form with no copy (not measured, claustrum's own).
func (u gitUse) atCommand() gitUse {
	f, _ := u.formNow(true)
	return f
}

// atGitDirCommand is atCommand for a call with --git-dir= in its argv.
func (u gitUse) atGitDirCommand() gitUse {
	f, _ := u.formNow(false)
	return f
}

// formNow is atCommand, and off reports that the off switch held the daemon
// when the form was taken. off is false for a request with no switch. ask is
// the ask of liveSwitch.gate.
func (u gitUse) formNow(ask bool) (f gitUse, off bool) {
	l := u.live
	if l == nil {
		return u, false
	}
	if l.off() {
		return gitUse{stop: l.stop, live: l, sshKept: u.sshKept}, true
	}
	if l.pending {
		l.pending = false
		if use, ok := l.gate(ask); ok {
			l.regated = &use
		}
	}
	if l.regated != nil && (!l.adopted || !u.shadow()) {
		return *l.regated, false
	}
	return u, false
}

// fixedForm is the form of a step of a create that starts now and keeps that
// form to its end: the `worktree add` (measured on Linux) and the checkout. f
// reads the switch no more. It keeps the held git directory of u, so the tests
// of that folder and the acts through it stay as they are. ask is the ask of
// liveSwitch.gate.
func (u gitUse) fixedForm(ask bool) (f gitUse, off bool) {
	f, off = u.formNow(ask)
	f.live = nil
	if f.accepted == nil && u.accepted != nil {
		f.accepted, f.gitDir = u.accepted, u.gitDir
	}
	return f, off
}

// inFolder reports whether the request keeps its short-lived folders in the
// folder of the daemon, and sends six profile values on its light calls. It
// does in the state shadow, and in the state fallback-git without the member.
//
// In the state fallback-git 5fd08069 names `<S>/.gitdir-<n>` in a git.status
// and `<S>/.index-<n>` in the read-tree of a create. Its light calls of a
// create and the listings of create and remove carry
// GIT_ALLOW_PROTOCOL=denied_by_claude_ssh, GIT_ASKPASS= and GIT_NO_LAZY_FETCH=1
// there. That is measured on Windows, macOS and Linux, also for the git.status
// folder. The macOS and Windows call logs sort the variables by name, so the
// order of the six values in that state is not measured: it is the order of the
// state shadow. The folder of the include step in that state is not measured:
// it is in the folder of the daemon too.
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
// 90fca6e6 on macOS, see worktreeGitText). leaf is the folder of the new
// worktree, and cp is the checkpoint of the create, which holds that folder
// open.
//
// In the state shadow the call gets a folder `.s<n>` of its own, and other
// steps follow it (runAddStaged). On Linux and macOS its listing carries
// GIT_COMMON_DIR=<copy>, as on 5fd08069 (measured). There, after the listing
// and before the add, the daemon makes the folder `worktrees` of the git
// directory when it is not there (makeWorktreesFolder), as 5fd08069 does
// (measured). be1c1ca5 stages the add on Linux and macOS. On Windows the staged
// add starts at once (worktreeAddStaged). With the member and in the state
// fallback-git the call names no copy, on every system: on Windows the add of
// 5fd08069 carries GIT_COMMON_DIR=<git directory> in the state fallback-git,
// and it names no `.s<n>` folder (measured).
//
// The call takes the form of the off switch at its start and keeps it to its
// end (fixedForm). On 5fd08069 an add that starts while the hold is live
// carries GIT_COMMON_DIR=<repo>/.git, and an add that started before the hold
// keeps the copy (measured on Linux). An add that starts under the hold keeps
// that form when the hold ends while it runs (measured). After a hold that
// ended before the add, the add names the copy, also in a create whose gate
// ran under the hold (measured). Such a create holds its git directory from
// that later gate on (keepLiveCreate), so a copy add acts through the held
// folder. An add with no copy in a create that holds its git directory gets
// the test of offWorktreesRefusal after the listing of the add. 5fd08069
// refuses there with that text, and the listing in the repository is its last
// call (measured on Linux).
//
// afterListing, when set, runs after the listing and the refusals that follow
// it, right before the add (pairBeforeAdd).
//
// offMember is true for a create with "shadowDirOff":true and no copy. Its add
// gets the same test after the listing. On 5fd08069 that listing comes after
// `rev-parse --absolute-git-dir` and is the last call (measured on Linux,
// macOS and Windows). A real folder there is the control case.
//
// The call holds the lock of the add step (lockWorktreeAddStep), so a remove in
// the same git directory deletes no registration while it runs. On Windows the
// lock also covers the staging steps (not measured). The lock comes before
// fixedForm, so an add that waits for a remove takes the form of the off switch
// at its start, and the gate of fixedForm runs under the lock.
func worktreeAdd(ctx context.Context, u gitUse, repo, leaf string, cp worktreeCheckpoint, offMember bool, afterListing func(), args ...string) (string, error) {
	defer lockWorktreeAddStep(repo)()
	f, off := u.fixedForm(true)
	_, named := f.names(repo)
	if named && stagedAddWindowsSteps {
		stderr, err := worktreeAddStaged(ctx, f, repo, leaf, cp, args...)
		if err == nil && f.accepted != nil {
			f.accepted.moved = true
		}
		return stderr, err
	}
	named = named && shadowLinksWorktrees
	cmd := hardenedGitCmd(ctx, f, repo, false, nil, args...)
	if offMember || (off && f.accepted != nil) {
		if text := offWorktreesRefusal(repo); text != "" {
			return "", &addNotPrepared{text: text, offLink: true}
		}
	}
	// A create that holds its git directory runs no `worktree add` when the
	// path no longer names the held folder. The test comes after the listing.
	if f.swapped("plain add") {
		// The text has the name of the git directory as the request spells it.
		gitDir := f.gitDir
		if f.accepted.sent != "" {
			gitDir = f.accepted.sent
		}
		return "", errors.New(plainAddRefusal(gitDir, named))
	}
	if named {
		if text := f.worktreesFolderRefusal(); text != "" {
			return "", &addNotPrepared{text: text}
		}
		f.makeWorktreesFolder()
		if text := f.worktreesOpenRefusal(); text != "" {
			return "", &addNotPrepared{text: text}
		}
	}
	if afterListing != nil {
		afterListing()
	}
	// On Linux and macOS an add that names the copy is staged as on Windows,
	// after the steps above (runAddStaged). be1c1ca5 does so on Linux and macOS.
	if named {
		stderr, err := runAddStaged(ctx, f, cmd, leaf, cp)
		if err == nil && f.accepted != nil {
			f.accepted.moved = true
		}
		return stderr, err
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := gitRun(ctx, cmd)
	if err == nil && f.accepted != nil {
		f.accepted.moved = true
	}
	return errBuf.String(), err
}

// offLinkListing runs one listing in repo in the form of an add (worktreeAdd),
// with no add after it. A create in attach mode runs it after the refusal of
// offWorktreesRefusal. In attach mode 5fd08069 runs two listings after
// `rev-parse --absolute-git-dir` and then answers (measured on Linux and
// macOS). The environment of the second listing is not compared.
func offLinkListing(ctx context.Context, u gitUse, repo string) {
	f, _ := u.fixedForm(true)
	hookPrecursor(ctx, f, repo, false)
}

// plainAddRefusal is the error of a `worktree add` that does not run, because
// the path of the git directory gitDir no longer names the folder that the
// create holds. named says whether the call names the copy of the request.
//
// On macOS `.git` becomes a symbolic link after `rev-parse
// --absolute-git-dir`. 5fd08069 then makes the listing of the add and no
// `worktree add` (measured on macOS):
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
// Linux has the same two texts (measured, 2 of 2 runs for each form). On
// Windows the call comes here only when it does not name the copy. The text
// is that of a junction that was put at `.git` after the listing of the add
// (measured on Windows).
func plainAddRefusal(gitDir string, named bool) string {
	switch {
	case stagedAddWindowsSteps:
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
// no `worktree add`, and no leaf and no branch stay (measured on Linux, 3 of 3
// runs). The test is where the daemon makes that folder, so it runs in the
// state shadow on Linux and macOS only. On Windows worktreesLinkRefusal gives
// its text before the add. The other states are not measured. In
// attach mode 5fd08069 answers the same text one time (measured on Linux): the
// caller makes no second add (addNotPrepared).
//
// A file at the name gets the same text (measured on Linux). The entry is
// read through the held git directory, or by the path for a request that
// holds none. claustrum's own, not measured: the text names the held git
// directory, with no cut of a long path.
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

// worktreesLinkRefusal is the text of worktreesFolderRefusal for a link or a
// junction at `worktrees` of the held git directory, by its Lstat through the
// held root. It is "" for a request that holds no git directory, and for a
// file there, whose text 5fd08069 gives at the add (measured on Windows).
func (u gitUse) worktreesLinkRefusal() string {
	if u.accepted == nil {
		return ""
	}
	if fi, err := u.accepted.root.Lstat(worktreesSubdir); err != nil || fi.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
		return ""
	}
	return u.worktreesFolderRefusal()
}

// worktreesOpenRefusal is the error of a `worktree add` that does not run,
// because the folder `worktrees` of the git directory is there and cannot be
// opened. It is "" in every other case. With that folder at mode 0300, 5fd08069
// answers `git worktree add failed: cannot prepare the repository's worktrees
// directory: openat worktrees: permission denied`. It runs no `worktree add`,
// and no leaf, branch or registration stays (measured on Linux and macOS). Only
// mode 0300 is measured. claustrum opens the folder through a root at the git
// directory, which gives that text. The open runs after makeWorktreesFolder. A
// folder that does not exist then keeps the earlier rule: the add runs (not
// measured).
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
// cannot list the repository's worktree entries: "worktrees" is a link or a file
// where Claude needs a real directory; put the directory itself in its place`.
// It runs no `worktree add`, makes nothing behind the link and leaves no branch,
// and R/.claude/worktrees stays (measured on Linux and macOS). An add that
// starts under a hold gets the same text, with equal replies (measured on Linux.
// The same path on Windows is not measured). A file at the name gets the same
// text (measured on Linux). On Windows a junction and a directory symbolic link
// there get it too, and 5fd08069 removes the leaf it made (measured on Windows).
// The folder is that of the git directory that the trust check pins. claustrum's
// own, not measured: a daemon GIT_COMMON_DIR, with which git writes elsewhere,
// and a file there on Windows.
func offWorktreesRefusal(repo string) string {
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
// it (measured on Linux and macOS, 6 of 6 runs).
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
// "worktrees", 0777)` and then the mode call (18 measured rows, Linux). The
// registration that git makes in the folder takes the set-group-ID bit from it:
// `d 2755` with umask 022.
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
// The create runs it after its tests of the new registration. After a refusal of
// those tests the file still names the copy on 5fd08069: `gitdir:
// <copy>/worktrees/w1` (measured on Linux). The same holds after the refusal of a
// git directory that is no longer the held folder (measured on Linux, 2 runs).
// Not measured: the order of this step and the test of the leaf identity (a
// measurement has one result for both orders).
//
// Since the add of the state shadow is staged (runAddStaged), git writes no
// `.git` file into the leaf, and the staging step writes one that names the
// git directory. So no file names the copy, and this step changes nothing.
// It stays as the guard of an add that names the copy without the staging
// step (from the code).
func (u gitUse) repointLeaf(repo, leaf string, cp worktreeCheckpoint) {
	if u.shadow() && shadowLinksWorktrees {
		repointWorktreeGitFile(u, leaf, cp)
	}
}

// repointWorktreeGitFile replaces the `.git` file of the new worktree leaf when
// its text names the registration through the folder of the copy.
//
// On Linux with git 2.43.0 the add of 5fd08069 writes `gitdir:
// <copy>/worktrees/<name>` and a newline into that file. 4 to 25 ms after the
// call ended, and before the next call, the file is a new file with the text
// `gitdir: <git directory>/worktrees/<name>` and a newline, in 6 of 6 runs
// (measured). On macOS with git 2.50.1 git writes the path in the repository
// itself, and the file keeps its text and its inode (measured).
//
// The file is replaced only after the daemon read it and its text is that one
// line: `gitdir: `, the folder of the copy, `/worktrees/`, one name with no
// separator, and a newline. Every other text is left as it is
// (replaceWorktreeGitFile). The new file is made with mode 0666, and the umask
// of the daemon takes its bits from that. On 5fd08069 the file is `f 0664`
// with a daemon umask of 002, in 13 of 13 creates (measured, Linux). With
// umask 022 it is `f 0644`, and with umask 000 it is `f 0666` (measured on
// Linux, 1 run for each umask).
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
	// swap (measured on macOS). With a link to a folder that is no git
	// directory it carries GIT_DIR=/dev/null (measured).
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
// after that call, its call 15, and makes nothing (measured on Linux, 3 of 3
// runs). With the member claustrum answers the same line, because git prints
// `false` there. 5fd08069 is not measured with the member.
//
// Which test of 5fd08069 refuses is not measured. It is not the `core.bare`
// value of the copy. A measurement has `bare = true` in the `config.worktree`
// file of a repository with a work tree. The copy then holds `bare = "true"` as
// its last `core` block, and the create of 5fd08069 succeeds (measured on
// Linux). claustrum tests what the walk of the trust check found: a git
// directory that no folder holds as its `.git` entry (gitWalkRoot). With a
// GIT_DIR in the daemon's own environment the walk does not say that, and the
// test does not run (not measured).
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
// them on Linux (measured). They show them on macOS (measured) and on Windows
// too. A bare repository has GIT_OBJECT_DIRECTORY=<repo>/objects (measured).
//
// They are the build of a git call: a folder of the copy that went is made
// again first (renewCopy).
func (u gitUse) commonVars() []string {
	u.renewCopy()
	return []string{"GIT_COMMON_DIR=" + u.repoDir, "GIT_OBJECT_DIRECTORY=" + filepath.Join(u.gitDir, "objects")}
}

// pin is the folder variables of a git call in dir with no --git-dir= in its
// argv. In the state shadow they are GIT_COMMON_DIR, GIT_OBJECT_DIRECTORY,
// GIT_DIR and GIT_WORK_TREE, in this order. Measurements show the order on
// Linux, and on macOS. Windows has no record of the order.
//
//   - GIT_DIR is the git directory of dir. For a linked worktree it is the entry
//     under `worktrees`. The two first variables are then those of the
//     repository (measured).
//   - GIT_WORK_TREE is the folder that holds the `.git` entry. A folder inside
//     the work tree gets the root of the work tree (measured on Linux). A bare
//     repository has no GIT_WORK_TREE (measured).
//
// With a GIT_DIR in the daemon's own environment, the trust check judges that
// directory, and the walk from dir does not find it. The call then gets the two
// first variables only. The daemon's own GIT_DIR stays at its place in the
// environment, before the profile values, and the call has no GIT_WORK_TREE. On
// 5fd08069 each such call has GIT_DIR=<the daemon's value>, the profile values,
// GIT_COMMON_DIR=<copy> and GIT_OBJECT_DIRECTORY=<that git directory>/objects,
// in this order (measured on Linux, 27 calls). A relative GIT_DIR of the daemon
// is not measured.
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
// Measurements show that on Linux, macOS and Windows.
func (u gitUse) gitDirPin(dir string) []string {
	if _, ok := u.names(dir); !ok {
		return commonDirPinEnv(dir)
	}
	// After a create moved its hold, a call with --git-dir= names the objects
	// of that folder (measured on Linux).
	u.renewCopy()
	return []string{"GIT_COMMON_DIR=" + u.repoDir, "GIT_OBJECT_DIRECTORY=" + filepath.Join(u.heldGitDir(), "objects")}
}

// profileEnv is profileEnv for a call of this request. In the state shadow a
// light call carries the six values of the heavy profile, in another order.
// 5fd08069 sends them so on the light calls of a create outside the new
// worktree, and on the listing before each. It sends them on the three
// listings of the branch step too. Measurements have the order of the Linux
// call log. A measurement has the same order. A measurement has the six values
// and no order. With the member those calls carry the four values of the light
// profile.
//
// The listings and the read-tree in the new worktree keep the four values in
// both forms (measured). They do not come here.
//
// The profile ends with GIT_SSH_COMMAND=false when sshOff allows it
// (withSSHOff).
func (u gitUse) profileEnv(heavy bool) []string {
	if heavy || (!u.inFolder() && !u.lightSix) {
		return withSSHOff(profileEnv(heavy), u.sshOff())
	}
	return withSSHOff([]string{
		"GIT_ALLOW_PROTOCOL=denied_by_claude_ssh",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_GRAFT_FILE=" + os.DevNull,
		"GIT_NO_LAZY_FETCH=1",
		"GIT_ASKPASS=",
	}, u.sshOff())
}

// sshOff reports whether a call of this request with GIT_NO_LAZY_FETCH=1 and
// GIT_ASKPASS= gets GIT_SSH_COMMAND=false. be1c1ca5 gives it to every such
// call of the five git methods in the state shadow. With "shadowDirOff":true
// git.info, git.status and git.list_branches keep it. The calls of a create
// and a remove that name no copy then keep the daemon's own value (measured
// on Linux). A call that names the copy gets it always, and so do the calls
// in the state fallback-git (from the code, not measured). With the member
// outside fallback-git the two pairs of a create get it too (pairSSHUse), and
// so does its settle step (settleCheckout).
func (u gitUse) sshOff() bool {
	return u.shadow() || u.sshKept == nil || !u.sshKept()
}

// precursorEnv is precursorEnv with the profile of this request.
func (u gitUse) precursorEnv(heavy bool, pin []string) []string {
	return precursorEnvWith(u.profileEnv(heavy), pin)
}

// hardenedGitEnv is hardenedGitEnv with the profile of this request.
func (u gitUse) hardenedGitEnv(heavy bool, pin, hooks []string) []string {
	return hardenedEnvWith(gitChildEnv(), u.profileEnv(heavy), pin, hooks)
}

// The names of the short-lived folders of one request. In the state shadow
// they are in the folder of the daemon, with a decimal number after the name.
// 5fd08069 has three of them:
//
//   - `.gitdir-<n>` for one git.status (measured on Linux and Windows).
//   - `.index-<n>` for the checkout of one create (measured). It has mode 0700
//     on Linux (measured). On Linux and macOS 4c111e86 names it
//     `.checkout-<n>` (checkoutDirPrefix, measured).
//   - `.worktreeinclude-<n>` with the file `patterns` for one include step
//     (measured).
//
// With the member the folders are in the temporary folder:
// `claude-ssh-gitdir-<n>`, `claude-ssh-checkout-<n>` and
// `claude-ssh-worktreeinclude-<n>`. The last one holds the file `patterns`
// (5fd08069, measured on Linux). The index folder is the one of 4c111e86
// (measured on Linux and macOS). c9125eb8 used `claude-ssh-index-<n>`.
const (
	shadowStatusDirPrefix  = ".gitdir-"
	shadowIndexDirPrefix   = ".index-"
	shadowIncludeDirPrefix = ".worktreeinclude-"
	includePatternsName    = "patterns"
)

// requestDirMkdir makes a short-lived folder of a request by its path on
// Windows (makeRequestDir). It is a variable so that a test can see the call.
var requestDirMkdir = os.Mkdir

// requestDir makes a new folder for one request and returns its path and the
// function that removes it. In the state shadow the folder is
// <S>/<shadowPrefix><n>, made with mode 0700 through a root at the folder of
// the daemon (by its path on Windows, see below), and removed through
// removeEntry. In every other state it is a new folder with tempPrefix in the
// temporary folder, as before.
//
// The shutdown of the daemon removes its folder with every such entry in it,
// also while the request runs (Close). 5fd08069 does so with a `.gitdir-<n>`
// in flight (measured on Linux).
//
// The error of a folder that cannot be made in the folder of the daemon names
// its full path, with the operation `mkdir`. With a folder of the daemon of
// mode 0500 a create of 5fd08069 answers `git worktree add failed (checkout):
// mkdir <S>/.index-<n>: permission denied` (measured on Linux). On Linux and
// macOS claustrum prints `.checkout-<n>` there. On Windows
// the folder `.s<n>` of a create gives `mkdir
// C:\o\w\run\git-shadow\<S>\.s<n>: Access is denied.` (measured on Windows).
// A folder of the daemon that cannot be opened is not measured: it gives the
// error of that open.
//
// On Windows each such folder is made by its full path with a plain mkdir,
// not through the root (makeRequestDir). On Windows with a deny entry for new
// folders on <S> and the backup and restore privileges enabled, the create of
// 5fd08069 succeeds. The root mkdir of claustrum failed there at `.s<n>`
// (measured) and at `.index-<n>`. With both privileges disabled both builds
// fail with the text above (measured). By path the create of claustrum
// succeeds (measured). The prefixes .config-, .worktreeinclude- and the
// .gitdir- of git.status are not measured there (claustrum's own). Linux and
// macOS keep the root.
func (u gitUse) requestDir(shadowPrefix, tempPrefix string) (dir string, remove func(), err error) {
	if !u.inFolder() {
		dir, err = os.MkdirTemp("", tempPrefix)
		if err != nil {
			return "", func() {}, err
		}
		return dir, func() { removeTempRequestDir(dir) }, nil
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
		err = makeRequestDir(root, u.folder, name)
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

// removeTempTree removes the temporary folder of a request as a tree. It is a
// variable so that a test can see the call and make it inert.
var removeTempTree = os.RemoveAll

// removeTempRequestDir removes dir, a folder that requestDir made with
// os.MkdirTemp in the temporary folder. homeguard.WipesHomeDir refuses a dir
// that is home or holds home first, as for each delete of the folder
// git-shadow (removeEntry). The guard is claustrum's own.
func removeTempRequestDir(dir string) {
	if homeguard.WipesHomeDir(dir) {
		return
	}
	_ = removeTempTree(dir)
}
