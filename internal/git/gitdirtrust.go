package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The git-directory trust check that every git.* method applies to the directory it runs git in, before
// git runs. It follows reference f6010b97. It was measured black box, side by side against that build,
// on Linux. Where a case can exist on macOS or Windows, it was measured on those systems too.
// docs/protocol/git-rules-trust.md → Git-directory trust check describes it.
//
// The check finds the git directory, then judges it. A linked-worktree entry must carry
// a `commondir` file that names its own repository. Any other git directory carries no
// `commondir`, or one that reads ".", "./" or "./.". A dangling relative symlink that stays
// in that directory counts as none (judgeStrayCommondir). Three verdicts come out:
//
//   - trusted: git runs with GIT_COMMON_DIR pinned to the repository git directory.
//     For a linked-worktree entry that is the repository the entry names. For any
//     other git directory it is that directory itself.
//   - no repository: the method answers as if the directory held no repository.
//   - refused: the method answers with one of the fixed texts below, and git never runs.

const (
	gitDirTrustLead   = "the repository's git directory could not be trusted; git not run: "
	gitDirTrustPrefix = gitDirTrustLead + "commondir not as git writes it: "
	// gitDirEntryGoneRefusal carries the older hooks prefix. It answers a daemon GIT_DIR
	// that names a linked-worktree entry which no longer exists.
	gitDirEntryGoneRefusal = hooksPinPrefix +
		"the worktree entry this folder's .git names no longer exists"
	// gitDirAddWrap prefixes a refusal on git.worktree_create when the daemon's own
	// environment already carries GIT_COMMON_DIR.
	gitDirAddWrap = "git worktree add failed: cannot locate the repository's git directory: "

	commondirMaxBytes = 1 << 20 // exactly 1 MiB passes, one byte more fails
	gitFileMaxBytes   = 1 << 20 // a larger `.git` file names no repository
	operandMaxRunes   = 300     // each quoted path is cut to this many runes first
	headReadMaxBytes  = 255     // HEAD is judged on its first 255 bytes only
)

type gitDirVerdict int

const (
	gitDirTrusted gitDirVerdict = iota
	gitDirNoRepo
	gitDirRefused
)

// gitDirTrust is the outcome of the check for one request directory.
type gitDirTrust struct {
	verdict gitDirVerdict
	// refusal is the full wire text when verdict is gitDirRefused.
	refusal string
	// pinCommonDir is the repository git directory of a trusted git directory. Git
	// runs with GIT_COMMON_DIR set to it, and a remove verifies and deletes entries
	// under it. For a daemon GIT_DIR that names nothing, it is the absolute form of
	// that GIT_DIR. It is empty when the check left the
	// directory to git with no pin.
	pinCommonDir string
	// configDir is the folder of the daemon's read of the config and of the hold of a
	// create, when it is not pinCommonDir (entryRepoThroughJunction). It is empty for
	// every other request.
	configDir string
	// noRepoEnv is what commonDirPinEnv returns in place of the pin when verdict is
	// gitDirNoRepo, so every hardened git call in that folder carries it, whatever the
	// method. The rows that measure it are removals (see noRepoPin). It is nil when no
	// row gives a value.
	noRepoEnv []string
	// unsearchable is true when the walk found the git directory and the daemon
	// cannot search it (gitDirUnsearchable). readGitDirTrust answers "no repository"
	// then.
	unsearchable bool
	// worktreeEntry is the git directory of a trusted linked worktree: the entry
	// under <repository git directory>/worktrees. It is empty for every other git
	// directory. The config read tests the config.worktree file there
	// (gitrepoconfig.go).
	worktreeEntry string
	// noGit is true when verdict is gitDirNoRepo and git.worktree_remove makes no git
	// call for a worktree folder that is gone. That is a daemon GIT_DIR that names a
	// file. 89cb6289 answers {"success":true,"branchKept":true} there with no git call
	// (measured on Linux and macOS).
	noGit bool
	// goneGitFile is true when verdict is gitDirNoRepo for a `.git` file that the
	// walk found and that names a missing path, which is not an entry under
	// `worktrees` of an existing git directory (isWorktreeEntry). git.info and
	// git.worktree_create run the start test there (gitInfo,
	// gitWorktreeCreateLocked).
	goneGitFile bool
	// fileGitDir is true when verdict is gitDirNoRepo for a `.git` file that the walk
	// found and that names a regular file (Linux and macOS). With a refused
	// GIT_CONFIG_* entry of the daemon, each method answers "no repository" there, as
	// b3081b51 does (measured on Linux and macOS).
	fileGitDir bool
	// goneEnvGitDir is true for a daemon GIT_DIR that names a missing path, on Linux
	// and macOS. A failed copy of the repository then answers "no repository"
	// (settingsGate), as b3081b51 does (measured on Linux and macOS). Windows keeps
	// the refusal (claustrum's own, not measured).
	goneEnvGitDir bool
}

// noRepoPin is the environment entry of a git call in a folder where the trust check
// finds no repository. The call logs of 89cb6289 show GIT_DIR=<null device> on each
// call of a removal there, the calls of the branch step included (measured on Linux,
// macOS and Windows, and on Linux with worktreeRoot). An empty `.git` folder inside
// an outer repository gets it too, so the branch of the outer repository stays
// (measured on Linux and macOS). Not measured: a `.git` file that names no git dir.
// claustrum sets the same value there.
func noRepoPin() []string {
	return []string{"GIT_DIR=" + os.DevNull}
}

// noRepoPinned reports whether a git call in the folder of t, a "no repository"
// verdict, carries the entry of noRepoEnv. It is false when the daemon's own
// environment sets GIT_COMMON_DIR, because commonDirPinEnv then adds no entry. It is
// false too when noRepoEnv is nil, which covers noGit. A method reads the answer of
// the listing in such a folder only when this is true. Without the entry git reads
// the `.git` file of the folder, and a request with no GIT_CONFIG_* entry gets a
// refusal that no row of 89cb6289 shows. Not measured: the frames of 89cb6289 in a
// "no repository" folder with a daemon GIT_COMMON_DIR. claustrum keeps the frames
// that it had before it read that listing.
func (t gitDirTrust) noRepoPinned() bool {
	return t.noRepoEnv != nil && !daemonCommonDirSet()
}

// quoteOperand cuts s to its first operandMaxRunes runes and quotes it with Go %q. An
// invalid UTF-8 byte counts as one rune and stays a byte, so %q prints it as \xNN.
func quoteOperand(s string) string {
	return quoteCut(s, operandMaxRunes)
}

// quoteCut is quoteOperand with the rune count n.
func quoteCut(s string, n int) string {
	i := 0
	for at := range s {
		if i == n {
			s = s[:at]
			break
		}
		i++
	}
	return fmt.Sprintf("%q", s)
}

// The refusals of a commondir in a git directory that is not a linked-worktree entry.
// 89cb6289 answers them on Linux, macOS and Windows (measured). The `<name>` in each
// text is literal.
const (
	strayContentTail = "; git writes that file only inside .git/worktrees/<name>/, and Claude Code's " +
		"sandbox may leave one holding \".\" or \"./.\"; anything else sends git's configuration and hooks " +
		"elsewhere, so treat it as tampering: delete it, and if you did not create it, find out what did"
	strayFileTail = "; git writes that file only inside .git/worktrees/<name>/; one here can send git's " +
		"configuration and hooks elsewhere, so treat it as tampering: delete it, and if you did not " +
		"create it, find out what did"
	// strayShapeTail is the text of build 52d82f0a (measured on Linux and Windows).
	strayShapeTail = " sits in a git directory git would not take as a repository (its HEAD, objects or " +
		"refs missing, invalid, or not open to your account on the remote machine), so git would " +
		"look for one somewhere else; repair that directory (usually its HEAD file)"
)

// refuseStrayContent refuses a stray commondir whose value (commondirValue) is not
// ".", "./" or "./.". The text does not quote the value (build 683cc6a9, measured on
// Linux). The text with "./." and strayContentTail are those of 4c111e86, measured on
// git.worktree_create on Linux and Windows. git.worktree_remove shows the text on
// Windows. git.info, git.status and git.list_branches share it (not measured).
func refuseStrayContent(cd string) gitDirTrust {
	return gitDirTrust{verdict: gitDirRefused, refusal: gitDirTrustPrefix + quoteOperand(cd) +
		` holds something other than ".", "./" or "./."` + strayContentTail}
}

// refuseStrayFile refuses a stray commondir that is not a small plain file.
func refuseStrayFile(cd, reason string) gitDirTrust {
	return gitDirTrust{verdict: gitDirRefused, refusal: gitDirTrustPrefix + quoteOperand(cd) +
		" is not a small plain file (" + reason + ")" + strayFileTail}
}

// refuseStrayShape refuses a stray commondir with trusted content in a directory that
// fails the git-directory test.
func refuseStrayShape(cd string) gitDirTrust {
	return gitDirTrust{verdict: gitDirRefused, refusal: gitDirTrustPrefix + quoteOperand(cd) + strayShapeTail}
}

// refuseDamagedRepository refuses the entry of a worktree whose commondir names its
// repository git directory repo, when repo fails the git-directory test. The text is
// that of build 52d82f0a for a repository whose `refs` was moved away (measured on
// Linux).
func refuseDamagedRepository(repo string) gitDirTrust {
	return gitDirTrust{verdict: gitDirRefused, refusal: gitDirTrustLead + "this worktree's repository, " +
		quoteOperand(repo) + ", has a missing or damaged HEAD, objects or refs, or your account on " +
		"the remote machine cannot open them; repair that directory or its permissions"}
}

func refuseForeignCommondir(cd, repo string) gitDirTrust {
	return gitDirTrust{verdict: gitDirRefused, refusal: gitDirTrustPrefix + quoteOperand(cd) +
		" does not name the entry's own repository, " + quoteOperand(repo) +
		"; restore the file to read ../.. or remove that worktree entry and add the worktree again"}
}

func refuseMissingCommondir(entry string) gitDirTrust {
	return gitDirTrust{verdict: gitDirRefused, refusal: gitDirTrustPrefix + "worktree entry " +
		quoteOperand(entry) + " has none (git always writes one there, containing ../..); the " +
		"entry is damaged or was not made by git — recreate the worktree with git worktree add, " +
		"or remove the entry"}
}

func refuseUnreadableCommondir(cd, reason string) gitDirTrust {
	return gitDirTrust{verdict: gitDirRefused, refusal: gitDirTrustPrefix + quoteOperand(cd) +
		" is not a small plain file (" + reason + "); remove it, or remove that worktree entry " +
		"and add the worktree again so git rewrites it"}
}

// daemonCommonDirSet reports whether the daemon's own environment carries
// GIT_COMMON_DIR. git.info and git.list_branches then skip the check.
func daemonCommonDirSet() bool {
	return os.Getenv("GIT_COMMON_DIR") != ""
}

// daemonCommonDirStays reports whether the daemon's own GIT_COMMON_DIR goes to
// the git calls of a request that has a copy, in the place of the copy. It does
// when the daemon has that variable and no GIT_DIR (not measured on 5fd08069).
//
// With both variables in the daemon environment 5fd08069 makes the folder of
// the repository and sends GIT_COMMON_DIR=<copy> on each call, as with GIT_DIR
// alone. Its five requests have the same call counts in both cells: 20, 2, 4,
// 11 and 2 (measured on Linux). Both variables name one git directory there.
// Two values that differ are not measured: they get the same rule.
func daemonCommonDirStays() bool {
	return daemonCommonDirSet() && os.Getenv("GIT_DIR") == ""
}

// daemonGitDirSkipped reports whether a request with a copy runs as with no
// GIT_DIR in the daemon's environment: on Windows, with an absolute daemon
// GIT_DIR that names a folder and no daemon GIT_COMMON_DIR (copyPinsWalkGitDir).
//
// 87375cc7 does so on Windows for a plain repository, for a folder behind a
// junction with GIT_DIR spelled through it, and with GIT_DIR at the real path.
// In the state shadow the calls of git.info, git.status on the repository, a
// create and a remove there follow the walk (measured on Windows). So does a
// remove as the first request of a daemon, and the five methods on a folder in
// no repository (measured on Windows, gitDirTrustFor). Each pinned call carries
// GIT_DIR and GIT_WORK_TREE of the walk, at their real paths. The calls with
// --git-dir= and the `--attr-source` probe carry no GIT_DIR. A git.info after
// a git.info on another repository makes the read of the config again.
// git.status of a linked worktree and git.list_branches on a repository are
// claustrum's own, not measured: the copy follows the
// walk only when the walk finds a trusted git directory. A relative GIT_DIR
// and one that names no folder keep the rules above (claustrum's own, not
// measured). The refusals of a regular file there are those of 5fd08069
// (TestGitDirTrustDaemonGitDirPlainFileWindows).
func daemonGitDirSkipped() bool {
	gd := os.Getenv("GIT_DIR")
	if !copyPinsWalkGitDir || gd == "" || daemonCommonDirSet() || !filepath.IsAbs(gd) {
		return false
	}
	fi, err := os.Stat(gd)
	return err == nil && fi.IsDir()
}

// copyTrust is the verdict t of the trust check on dir as the copy of a request
// takes it. With daemonGitDirSkipped a trusted t gives way to the verdict of the
// walk from dir, when that walk finds a trusted git directory. The refusals of
// the check stay those of the daemon's GIT_DIR.
func copyTrust(t gitDirTrust, dir string) gitDirTrust {
	if !daemonGitDirSkipped() || t.verdict != gitDirTrusted || t.pinCommonDir == "" {
		return t
	}
	if w := trustWithGitDir(dir, ""); w.verdict == gitDirTrusted && w.pinCommonDir != "" {
		return w
	}
	return t
}

// requestGitDirTrust runs the check for one method. readMethod is true for git.info
// and git.list_branches, which skip the check when the daemon's environment carries
// GIT_COMMON_DIR. git.status, git.worktree_create and git.worktree_remove check anyway.
func requestGitDirTrust(dir string, readMethod bool) gitDirTrust {
	if readMethod && daemonCommonDirSet() {
		return gitDirTrust{}
	}
	return gitDirTrustFor(dir)
}

// commonDirPinEnv returns the GIT_COMMON_DIR pin for a git command run in dir, or nil.
// The pin applies to every trusted git directory, and only when the daemon's own
// environment does not already set GIT_COMMON_DIR.
func commonDirPinEnv(dir string) []string {
	return commonDirPinOf(gitDirTrustFor(dir))
}

// commonDirPinOf is commonDirPinEnv for the verdict t.
func commonDirPinOf(t gitDirTrust) []string {
	if daemonCommonDirSet() {
		return nil
	}
	// A git directory that the daemon cannot search gets the pin of "no
	// repository", as readGitDirTrust answers there (claustrum's own, not measured).
	if t.unsearchable {
		return noRepoPin()
	}
	if t.verdict == gitDirTrusted && t.pinCommonDir != "" {
		return []string{"GIT_COMMON_DIR=" + t.pinCommonDir}
	}
	if t.verdict == gitDirNoRepo {
		return t.noRepoEnv
	}
	return nil
}

// gitDirTrustFor finds and judges the git directory for dir. A dir that does not
// resolve to an existing directory is left to git, which answers for itself.
//
// With daemonGitDirSkipped a dir in which the walk finds no repository gets the
// verdict of the walk. In the state shadow 87375cc7 answers git.info, git.status,
// git.list_branches, a create and a remove on such a folder with the calls of a
// daemon with no GIT_DIR: the listing and the next call carry GIT_DIR=NUL
// (measured on Windows). A dir in which the walk finds another trusted git
// directory gets the verdict of the walk too: 87375cc7 runs git.info, a create
// and a remove in a second repository with the calls of that repository and
// none of the daemon's (measured on Windows). A walk that finds the folder of
// the daemon's GIT_DIR keeps the daemon verdict, with its spelling (f6010b97,
// measured on Windows for the pin with no copy, TestGitDirTrustShortNamePinWindows). git.info with "shadowDirOff":true
// keeps the daemon verdict (daemonGitDirVerdict): measured on Windows for a
// folder in no repository, claustrum's own in a repository. The other methods
// with that switch, and the state fallback-git, follow the walk (claustrum's
// own, not measured).
func gitDirTrustFor(dir string) gitDirTrust {
	t := daemonGitDirVerdict(dir)
	if daemonGitDirSkipped() && t.verdict == gitDirTrusted {
		if w := trustWithGitDir(dir, ""); w.verdict == gitDirNoRepo || (w.verdict == gitDirTrusted && w.pinCommonDir != "" && !sameProbeFolder(w.pinCommonDir, t.pinCommonDir)) {
			return w
		}
	}
	return t
}

// daemonGitDirVerdict is gitDirTrustFor without the walk of daemonGitDirSkipped:
// the verdict of the daemon's GIT_DIR when it is set.
func daemonGitDirVerdict(dir string) gitDirTrust {
	return trustWithGitDir(dir, os.Getenv("GIT_DIR"))
}

// trustWithGitDir is gitDirTrustFor with gd in the place of the daemon's GIT_DIR.
// An empty gd walks from dir.
func trustWithGitDir(dir, gd string) gitDirTrust {
	if dir == "" {
		dir = "."
	}
	start, err := walkStart(dir)
	if err != nil {
		return gitDirTrust{}
	}
	if start, err = filepath.Abs(start); err != nil {
		return gitDirTrust{}
	}
	if fi, err := os.Stat(start); err != nil || !fi.IsDir() {
		return gitDirTrust{}
	}
	// A GIT_DIR in the daemon's environment is inherited by git, so that directory is
	// the one judged, for every request directory. A relative value is taken relative
	// to the request directory, as git takes it.
	if gd != "" {
		if !filepath.IsAbs(gd) {
			gd = filepath.Join(start, gd)
		}
		gd = realParentPath(gd)
		t := judgeGitDir(gd, true)
		t.unsearchable = gitDirUnsearchable(gd)
		return t
	}
	g, _, ok := findGitDir(start)
	if !ok {
		return gitDirTrust{verdict: gitDirNoRepo, noRepoEnv: noRepoPin()}
	}
	t := judgeGitDir(g, false)
	t.unsearchable = gitDirUnsearchable(g)
	return t
}

// gitDirUnsearchable reports whether g is a folder that the daemon has no
// permission to search. The test is an Lstat of `<g>/.`, which needs that
// permission.
func gitDirUnsearchable(g string) bool {
	if fi, err := os.Stat(g); err != nil || !fi.IsDir() {
		return false
	}
	_, err := os.Lstat(g + string(filepath.Separator) + ".")
	return errors.Is(err, fs.ErrPermission)
}

// readGitDirTrust is requestGitDirTrust for git.info, git.status,
// git.list_branches and git.worktree_create. A git directory that the daemon
// cannot search counts as no repository there first, before any trust refusal.
// For a daemon GIT_DIR that names a folder of mode 0600, git.info and git.status
// answer so (measured on Linux). For a `.git` file that names such a folder, all
// four answer so (683cc6a9, measured on Linux). The create answers not_a_repo
// there, with and without worktreeRoot. A daemon GIT_DIR on git.list_branches
// and the create, a linked-worktree entry, macOS and Windows are claustrum's own,
// not measured. With a daemon GIT_COMMON_DIR the verdict stays (claustrum's own,
// not measured), because no git call could carry the pin of noRepoPin then
// (noRepoPinned). The remove keeps the refusal of the trust check and deletes
// nothing (683cc6a9, measured on Linux).
func readGitDirTrust(dir string, readMethod bool) gitDirTrust {
	t := requestGitDirTrust(dir, readMethod)
	if t.unsearchable && !daemonCommonDirSet() {
		return gitDirTrust{verdict: gitDirNoRepo, noRepoEnv: noRepoPin()}
	}
	return t
}

// resolveWalkLinks is filepath.EvalSymlinks behind a seam, so a test on any system can
// give walkStart the answer that Windows gives for a junction.
var resolveWalkLinks = filepath.EvalSymlinks

// walkStart is where the walk of the trust check and the walk for the git.info root
// start: dir with its symlinks resolved. On Linux and macOS a dir that does not
// resolve has no start.
//
// On Windows a dir with a junction, a directory symlink or a link to a subst drive in it starts at its real path (linkFreePath).
// So a junction <P>\J into a second repository R finds R, on <P>\J\sub and on
// <K>\J\sub with a junction K to P (4c111e86 and 87375cc7, measured on Windows). A dir
// that has no junction and does not resolve, and is a directory, starts as it is
// spelled, cleaned (walkStartsAsSpelled). A missing name before `..` is such a dir. The
// paths <P>\missing\.. and <P>\missing\..\J\sub are equal to 89cb6289 in frame, disk and
// every git call (measured).
func walkStart(dir string) (string, error) {
	if final, ok := linkFreePath(dir); ok {
		if fi, err := os.Stat(final); err == nil && fi.IsDir() {
			return final, nil
		}
	}
	start, err := resolveWalkLinks(dir)
	if err == nil || !walkStartsAsSpelled {
		return start, err
	}
	if fi, statErr := os.Stat(dir); statErr != nil || !fi.IsDir() {
		return "", err
	}
	return filepath.Clean(dir), nil
}

// pathHoldsJunction is holdsJunction behind a seam, so a test on any system can
// stand in for a Windows junction.
var pathHoldsJunction = holdsJunction

// holdsJunction reports whether a name of the absolute path p is a junction. Go
// reports a junction as an irregular file, and only a junction or a symbolic link
// has a target to read (readLinkTarget). So another reparse tag is no junction. A
// name that cannot be read ends the test.
func holdsJunction(p string) bool {
	vol := filepath.VolumeName(p)
	cur := vol + string(filepath.Separator)
	for _, name := range strings.FieldsFunc(p[len(vol):], isPathSep) {
		cur = filepath.Join(cur, name)
		fi, err := os.Lstat(cur)
		if err != nil {
			return false
		}
		if fi.Mode()&fs.ModeIrregular != 0 {
			if _, ok := readLinkTarget(cur); ok {
				return true
			}
		}
	}
	return false
}

// junctionFreePath is the path of an open handle of p when p has a junction in it,
// on Windows: the real path, with long names and the letter case on disk. 4c111e86
// and 87375cc7 use that path (measured on Windows). ok is false off Windows, for a path
// with no junction and for a path that does not open, such as a dangling junction.
func junctionFreePath(p string) (final string, ok bool) {
	if !walkStartsAsSpelled {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil || !pathHoldsJunction(abs) {
		return "", false
	}
	final, err = finalLeafPath(abs)
	return final, err == nil
}

// pathHoldsLink is holdsLink behind a seam, so a test on any system can stand in for
// a Windows link.
var pathHoldsLink = holdsLink

// holdsLink reports whether a name of the absolute path p is a junction or a
// symbolic link (folderIsLink).
func holdsLink(p string) bool {
	vol := filepath.VolumeName(p)
	cur := vol + string(filepath.Separator)
	for _, name := range strings.FieldsFunc(p[len(vol):], isPathSep) {
		cur = filepath.Join(cur, name)
		fi, err := os.Lstat(cur)
		if err != nil {
			return false
		}
		if folderIsLink(fi.Mode()) {
			return true
		}
	}
	return false
}

// linkFreePath is the path of an open handle of p when p has a link in it, on
// Windows. After a chain whose first hop is a directory symbolic link, and for a link
// to a subst drive, 4c111e86 and 87375cc7 run git in the real long path (measured on
// Windows). ok is false as for junctionFreePath.
func linkFreePath(p string) (final string, ok bool) {
	if !walkStartsAsSpelled {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil || !pathHoldsLink(abs) {
		return "", false
	}
	final, err = finalLeafPath(abs)
	return final, err == nil
}

// realParentPath is p with the folder that holds its last name at its real path
// (junctionFreePath), and p itself when that folder has no junction. The last name
// is kept, so a link at that name stays a link for the tests that refuse it.
func realParentPath(p string) string {
	if parent, ok := junctionFreePath(filepath.Dir(p)); ok {
		return filepath.Join(parent, filepath.Base(p))
	}
	return p
}

// requestJunctionTarget is lastNameJunctionTarget behind a seam, so a test on any
// system can stand in for a Windows junction.
var requestJunctionTarget = lastNameJunctionTarget

// entryRepoThroughJunction is the target of repo, the repository folder that a
// linked worktree's `.git` file names, when repo is a junction. With `gitdir:
// <W>\J\worktrees\wl` and a junction J to R\.git, 4c111e86 and 87375cc7 keep J in
// GIT_DIR and answer all five methods (measured on Windows). The daemon reads the
// config and holds the git directory at the target (gitDirTrust.configDir). A
// junction named `.git` keeps its link refusal (claustrum's own, not measured). A
// daemon GIT_DIR reaches here at the real path of its folder (realParentPath).
func entryRepoThroughJunction(repo string, fromEnv bool) string {
	if fromEnv || filepath.Base(repo) == ".git" {
		return ""
	}
	target, ok := requestJunctionTarget(repo)
	if !ok {
		return ""
	}
	return target
}

// holdFolderFor is the folder that a create holds for the git directory gitDir: the
// target of a junction that a linked worktree's `.git` file names
// (entryRepoThroughJunction), and gitDir itself for every other request.
func (t gitDirTrust) holdFolderFor(gitDir string) string {
	if t.configDir != "" {
		return t.configDir
	}
	return gitDir
}

// gitWalkRoot is the root of git.info on Linux and macOS. It is the folder where a walk
// like that of the trust check finds the repository. That folder holds the `.git`
// directory or `.git` file. It is spelled as the walk has it: dir after walkStart
// (filepath.EvalSymlinks there) and filepath.Abs, then walked up. EvalSymlinks keeps the
// spelling of every component that is not a symlink, so letter case and Unicode form
// stay as sent (measured on macOS). The daemon's GIT_DIR and GIT_WORK_TREE do not change
// it (measured). It is "" when the walk found a git directory with no work tree (a bare
// repository, a path inside .git) or nothing. gitDir is the git directory the walk
// found. 89cb6289 takes its root so on Linux and macOS (measured).
//
// Not measured: the spelling when one component is a symlink and another a case
// variant.
func gitWalkRoot(dir string) (root, gitDir string) {
	if dir == "" {
		dir = "."
	}
	start, err := walkStart(dir)
	if err != nil {
		return "", ""
	}
	if start, err = filepath.Abs(start); err != nil {
		return "", ""
	}
	g, holder, ok := findGitDir(start)
	if !ok {
		return "", ""
	}
	return holder, g
}

// findGitDir walks up from start the way git discovers a repository. It returns false
// for "no repository". Unlike git, a `.git` directory that fails the git-directory test
// and holds no commondir ends the walk: git itself walks on to an outer repository.
// holder is the folder that holds the `.git` directory or file, or "" when the git
// directory found is the folder itself (a bare repository, a path inside .git).
func findGitDir(start string) (g, holder string, ok bool) {
	for d := start; ; {
		dotGit := filepath.Join(d, ".git")
		fi, err := os.Stat(dotGit)
		switch {
		case err != nil:
			// No `.git` here: the directory itself can be a bare git directory.
			if isGitDirShape(d) {
				return d, "", true
			}
		case fi.IsDir():
			if isGitDirShape(dotGit) {
				return dotGit, d, true
			}
			if _, err := os.Lstat(filepath.Join(dotGit, "commondir")); err == nil {
				return dotGit, d, true // judged next as a stray commondir
			}
			return "", "", false
		case fi.Mode().IsRegular():
			target, ok := readWalkGitFile(dotGit, d)
			if !ok {
				return "", "", false
			}
			return target, d, true
		}
		// A `.git` of another type (a FIFO, a device) is skipped.
		parent := filepath.Dir(d)
		if parent == d {
			return "", "", false
		}
		d = parent
	}
}

// readGitFile reads a `.git` file. It must start with the exact bytes "gitdir:" and be
// at most gitFileMaxBytes long. The rest is trimmed of white space and, when relative,
// taken relative to holder, the directory that holds the `.git` file.
func readGitFile(path, holder string) (string, bool) {
	// On Linux and macOS the open does not block and takes a regular file only, so a
	// FIFO put there after the walk looked is judged at once. On Windows it is the
	// plain open (openGitPlainFile).
	f, err := openGitPlainFile(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, gitFileMaxBytes+1))
	if err != nil || len(b) > gitFileMaxBytes || !bytes.HasPrefix(b, []byte("gitdir:")) {
		return "", false
	}
	target := strings.Trim(string(b[len("gitdir:"):]), " \t\r\n")
	if target == "" {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(holder, target)
	}
	return target, true
}

// readWalkGitFile reads a `.git` file that the walk of findGitDir found, and returns
// the git directory that it names. The parse is claustrum's own, not measured:
//   - The file is at most gitFileMaxBytes long.
//   - It starts with the exact bytes "gitdir: ", with one blank.
//   - Each trailing CR and LF goes. Other trailing white space stays. An empty rest
//     names nothing.
//   - Then the rest is cut at its first NUL byte.
//   - A path that is not absolute (filepath.IsAbs) is joined to holder. So on Windows
//     "gitdir: /x" is joined. A path that the NUL cut makes empty names holder.
//   - An absolute path stays as the file spells it, as readGitFile keeps it. The calls
//     pass it on as GIT_COMMON_DIR, so on Windows its forward slashes stay.
//
// The outcomes of git.worktree_remove for a NUL or a blank in that file follow the
// reference (measured on Linux). The join to holder, the folder of the `.git` entry
// as the walk spells it, is claustrum's own for a `.git` that is a symbolic link (not
// measured).
func readWalkGitFile(path, holder string) (string, bool) {
	f, err := openGitPlainFile(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, gitFileMaxBytes+1))
	if err != nil || len(b) > gitFileMaxBytes {
		return "", false
	}
	rest, ok := bytes.CutPrefix(b, []byte("gitdir: "))
	if !ok {
		return "", false
	}
	rest = bytes.TrimRight(rest, "\r\n")
	if len(rest) == 0 {
		return "", false
	}
	if i := bytes.IndexByte(rest, 0); i >= 0 {
		rest = rest[:i]
	}
	target := string(rest)
	if filepath.IsAbs(target) {
		return target, true
	}
	return filepath.Join(holder, target), true
}

// judgeGitDir judges the git directory g. fromEnv is true when the daemon's GIT_DIR
// named g, false when findGitDir found it.
func judgeGitDir(g string, fromEnv bool) gitDirTrust {
	fi, err := os.Stat(g)
	if err != nil {
		// Gone. A GIT_DIR that names a gone linked-worktree entry is refused. Any other
		// GIT_DIR is left to git, with GIT_COMMON_DIR pinned to its absolute form. A
		// relative GIT_DIR is joined to the request directory first. f6010b97 pins that
		// value on Linux and Windows (measured, also where GIT_DIR=/dev/null names
		// nothing on Windows). A directory found by the walk means no repository.
		if fromEnv {
			if isWorktreeEntry(g) {
				return gitDirTrust{verdict: gitDirRefused, refusal: gitDirEntryGoneRefusal}
			}
			return gitDirTrust{pinCommonDir: g,
				goneEnvGitDir: errors.Is(err, fs.ErrNotExist) && runtime.GOOS != "windows"}
		}
		// A `.git` file that names a git dir which is gone. The calls of a removal
		// carry GIT_COMMON_DIR=<that git dir> on 89cb6289 (measured on Linux and
		// macOS, with and without worktreeRoot). goneGitFile follows the measured
		// cases. Another stat error than a missing path is claustrum's own, not
		// measured: it runs no test.
		//
		// A gone target with the shape of a linked-worktree entry gets the entry of
		// noRepoPin in its place. On c8e2f083 and 5fd08069 git.info and git.status on
		// such a folder list with GIT_DIR=<null device> (measured on Linux).
		env := []string{"GIT_COMMON_DIR=" + g}
		if isWorktreeEntry(g) {
			env = noRepoPin()
		}
		return gitDirTrust{verdict: gitDirNoRepo, noRepoEnv: env,
			goneGitFile: errors.Is(err, fs.ErrNotExist) && !isWorktreeEntry(g)}
	}
	if !fi.IsDir() && nonDirGitDirIsNoRepo {
		return gitDirTrust{verdict: gitDirNoRepo, noGit: fromEnv, fileGitDir: !fromEnv && fi.Mode().IsRegular()}
	}
	g = resolveGitDirLinks(g)
	cd := filepath.Join(g, "commondir")
	if !isWorktreeEntry(g) {
		if t, refused := judgeDamagedRepository(g); refused {
			return t
		}
		if t, refused := judgeStrayCommondir(g, cd); refused {
			return t
		}
		// Pinned to itself. Where g is a file (Windows only, see
		// nonDirGitDirIsNoRepo), the daemon's own read of the git directory refuses
		// it before git runs (readRepoConfig and gitDirFilesDetail), without
		// "shadowDirOff":true (not measured with it), as 5fd08069
		// does.
		return gitDirTrust{pinCommonDir: g}
	}
	repo := filepath.Dir(filepath.Dir(g))
	_, err = os.Lstat(cd)
	if t, refused := unexaminedGitDir(err); refused {
		return t
	}
	if err != nil {
		// No commondir: a failed Stat of the entry other than "does not exist" gets
		// the could-not-be-examined text too (claustrum's own, not measured).
		_, err := os.Stat(g)
		if t, refused := unexaminedGitDir(err); refused {
			return t
		}
		return refuseMissingCommondir(g)
	}
	content, reason, _, osErr := readCommondirAt(g)
	if reason != "" {
		// The text drops the operation and path of the OS error, as in
		// judgeStrayCommondir. b3081b51 drops them for a mode-000 file on Linux
		// and a dangling relative link on Windows (measured), and for "path escapes from
		// parent" on Linux and macOS (measured, be1c1ca5 too). Other OS errors are
		// claustrum's own, not measured.
		var pe *fs.PathError
		if errors.As(osErr, &pe) {
			reason = pe.Err.Error()
		}
		return refuseUnreadableCommondir(cd, reason)
	}
	// A string comparison: no symlink in the value is resolved. On Windows the
	// comparison also ignores letter case and the slash direction.
	if !sameGitDirPath(commondirPath(g, content), repo) {
		return refuseForeignCommondir(cd, repo)
	}
	return gitDirTrust{pinCommonDir: repo, worktreeEntry: g, configDir: entryRepoThroughJunction(repo, fromEnv)}
}

// commondirValue is the value of the content b of a commondir file. Every trailing CR
// and LF goes, and the value ends at its first NUL byte. Nothing else is trimmed. So
// `../..` with a tab, with a space or after a NUL is no `../..`. Build 52d82f0a reads
// so in git.info on Linux and Windows, and in git.status on Linux. macOS, the stray
// read and the order of the two steps are claustrum's own, not measured. The remove
// and the create keep their own reads of a registration's commondir
// (strings.TrimSpace). On the remove, blanks, a tab, CR LF or a NUL after `../..`
// in that file refuse nothing (build 52d82f0a, measured on Linux, macOS and
// Windows). The create read is not measured.
func commondirValue(b string) string {
	v := strings.TrimRight(b, "\r\n")
	if i := strings.IndexByte(v, 0); i >= 0 {
		v = v[:i]
	}
	return v
}

// commondirPath is the path that the content of <dir>/commondir names: its value,
// cleaned when absolute, else joined to dir.
func commondirPath(dir, content string) string {
	v := commondirValue(content)
	if filepath.IsAbs(v) {
		return filepath.Clean(v)
	}
	return filepath.Join(dir, v)
}

// judgeDamagedRepository refuses g when its parent is named "worktrees", its own name
// is not ".git", its grandparent fails the git-directory test and its commondir, a
// small plain file, names that grandparent. Build 52d82f0a answers so for a commondir
// of `../..` (measured on Linux). Each other commondir that names the grandparent is
// claustrum's own, not measured. Any other g goes on to judgeStrayCommondir.
func judgeDamagedRepository(g string) (gitDirTrust, bool) {
	parent := filepath.Dir(g)
	if filepath.Base(parent) != "worktrees" || filepath.Base(g) == ".git" {
		return gitDirTrust{}, false
	}
	repo := filepath.Dir(parent)
	// An entry with no commondir whose grandparent fails only the search test, or
	// whose `objects` or `refs` is a file, gets the missing-commondir refusal, as
	// the existence test gave before (claustrum's own, not measured).
	if _, err := os.Lstat(filepath.Join(g, "commondir")); errors.Is(err, fs.ErrNotExist) {
		_, err := os.Stat(g)
		if t, refused := unexaminedGitDir(err); refused {
			return t, true
		}
		if partsExist(repo) && headLooksValid(filepath.Join(repo, "HEAD")) {
			return refuseMissingCommondir(g), true
		}
		return gitDirTrust{}, false
	}
	content, reason, openErr, _ := readCommondirAt(g)
	if openErr != nil || reason != "" {
		return gitDirTrust{}, false
	}
	if !sameGitDirPath(commondirPath(g, content), repo) {
		return gitDirTrust{}, false
	}
	return refuseDamagedRepository(repo), true
}

// isWorktreeEntry reports whether g is a linked-worktree entry: its parent is named
// "worktrees", its own name is not ".git", and its grandparent passes the git-directory
// test. That covers the entries of a main repository, a bare repository and a submodule.
func isWorktreeEntry(g string) bool {
	parent := filepath.Dir(g)
	return filepath.Base(parent) == "worktrees" && filepath.Base(g) != ".git" &&
		isGitDirShape(filepath.Dir(parent))
}

// judgeStrayCommondir judges <g>/commondir, where g is a git directory that is not a
// linked-worktree entry: a main `.git`, a bare repository, or a submodule's git
// directory. Git writes no commondir there. 89cb6289 takes these steps on Linux, macOS
// and Windows (measured):
//
//  1. No commondir: g is trusted. A dangling symlink whose target is relative and
//     stays in g counts as none when g passes the git-directory test (measured with the
//     target `nothing-here`). When g fails that test, the symlink is refused with the
//     shape text of step 4. 89cb6289 answers so for a HEAD that reads "garbage" on
//     Linux (measured).
//  2. A commondir that is not a small plain file is refused with its reason (measured).
//     A dangling symlink that is absolute, or that leaves g, is refused here with "path
//     escapes from parent" (measured on Linux). Their targets were `/nonexistent` and
//     `../nothing`. 683cc6a9 drops `openat commondir: `.
//  3. The content with every trailing CR and LF removed is exactly ".", "./" or "./."
//     ("./." since 4c111e86, measured on git.worktree_create on Linux, macOS and
//     Windows, and not measured on the other methods that share the step). A space
//     or a tab is not trimmed in the measured rows. Anything else is refused (quoted there, unquoted since 683cc6a9)
//     (measured). The content test comes before the shape test. The value also ends at its first NUL
//     (commondirValue). That cut is claustrum's own here, not measured.
//  4. g then passes the git-directory test, or it is refused (measured).
//
// It reports false when g is trusted. Not measured: leading CR or LF, a lone CR and
// ".//". Nor are the order of a reason and a bad HEAD, and a dangling symlink in a git
// directory with missing objects or refs. Two cases that PROTOCOL.md once refused with
// the older text were not run on 89cb6289. One is an entry whose grandparent fails the
// git-directory test, which judgeDamagedRepository takes first. The other is a
// Windows entry named through 8.3 short names, whose content "../.." step 3 refuses.
func judgeStrayCommondir(g, cd string) (gitDirTrust, bool) {
	if _, err := os.Lstat(cd); errors.Is(err, fs.ErrNotExist) {
		return gitDirTrust{}, false
	} else if t, refused := unexaminedGitDir(err); refused {
		return t, true
	}
	content, reason, openErr, osErr := readCommondirAt(g)
	switch {
	case errors.Is(openErr, fs.ErrNotExist):
		if !isGitDirShape(g) {
			return refuseStrayShape(cd), true
		}
		return gitDirTrust{}, false
	case reason != "":
		// The text drops the operation and path of the OS error (build 683cc6a9,
		// measured on Linux for "path escapes from parent"). The other OS errors,
		// the failed OpenRoot included, follow the same rule here: claustrum's own,
		// not measured.
		var pe *fs.PathError
		if errors.As(osErr, &pe) {
			reason = pe.Err.Error()
		}
		return refuseStrayFile(cd, reason), true
	}
	v := commondirValue(content)
	if v != "." && v != "./" && v != "./." {
		return refuseStrayContent(cd), true
	}
	if !isGitDirShape(g) {
		return refuseStrayShape(cd), true
	}
	return gitDirTrust{}, false
}

// unexaminedGitDir is the refusal for err, a failed Lstat of <G>/commondir, or a failed
// Stat of an entry-shaped G whose commondir is missing. It is false for no error and
// for "does not exist". The text carries the OS error without its operation and path.
// For "file name too long" of the Lstat it is that of 683cc6a9 (measured on Linux with
// a G of 4086 and 4087 bytes). Each other error, the Stat, macOS and Windows are
// claustrum's own, not measured. On git.info, git.status and git.list_branches a G
// that the walk found and the daemon cannot search is no repository first
// (readGitDirTrust).
func unexaminedGitDir(err error) (gitDirTrust, bool) {
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return gitDirTrust{}, false
	}
	text := err.Error()
	var pe *fs.PathError
	if errors.As(err, &pe) {
		text = pe.Err.Error()
	}
	return gitDirTrust{verdict: gitDirRefused, refusal: hooksPinPrefix +
		"this repository's git directory could not be examined (" + text +
		"); if .git is a file, check the path written in it"}, true
}

// readCommondirAt reads <dir>/commondir. A non-empty reason means the file is not a
// small plain file. It must be a regular file of at most commondirMaxBytes. A symlink
// is followed only when the link is relative and stays inside dir. openErr is the
// error of the open of the file, so a caller can tell a dangling symlink apart. osErr
// is the OS error behind a reason, or nil when the reason is not an OS error.
func readCommondirAt(dir string) (content, reason string, openErr, osErr error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err.Error(), nil, err
	}
	defer func() { _ = root.Close() }()
	// Opened without blocking, so a FIFO is judged at once instead of waiting for a
	// writer.
	f, err := root.OpenFile("commondir", os.O_RDONLY|openNonBlocking, 0)
	if err != nil {
		return "", err.Error(), err, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", err.Error(), nil, err
	}
	if !fi.Mode().IsRegular() {
		return "", "commondir is not a regular file", nil, nil
	}
	// be1c1ca5 names the file in quotes and the bound as 1 MiB, in a worktree entry
	// and in a stray commondir. 1048576 bytes passes (measured on Linux).
	const tooLarge = `"commondir" is larger than 1 MiB`
	if fi.Size() > commondirMaxBytes {
		return "", tooLarge, nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(f, commondirMaxBytes+1))
	if err != nil {
		return "", err.Error(), nil, err
	}
	if len(b) > commondirMaxBytes {
		return "", tooLarge, nil, nil
	}
	return string(b), "", nil, nil
}

// isGitDirShape is the git-directory test: `objects` and `refs` pass gitDirPartUsable
// and HEAD passes headLooksValid.
func isGitDirShape(d string) bool {
	for _, name := range []string{"objects", "refs"} {
		if !gitDirPartUsable(filepath.Join(d, name)) {
			return false
		}
	}
	return headLooksValid(filepath.Join(d, "HEAD"))
}

// partsExist reports whether `objects` and `refs` of d exist, of any type.
func partsExist(d string) bool {
	for _, name := range []string{"objects", "refs"} {
		if _, err := os.Stat(filepath.Join(d, name)); err != nil {
			return false
		}
	}
	return true
}

// headLooksValid judges HEAD. A symlink passes when its target text starts with
// "refs/". A regular file passes when its first headReadMaxBytes bytes hold "ref:",
// optional white space and then "refs/", or start with 40 hex digits. Anything else
// fails at once, including a FIFO.
func headLooksValid(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(p)
		return err == nil && strings.HasPrefix(target, "refs/")
	}
	if !fi.Mode().IsRegular() {
		return false
	}
	f, err := os.OpenFile(p, os.O_RDONLY|openNonBlocking, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	buf := make([]byte, headReadMaxBytes)
	n, _ := io.ReadFull(f, buf)
	return headContentValid(buf[:n])
}

func headContentValid(b []byte) bool {
	if rest, ok := bytes.CutPrefix(b, []byte("ref:")); ok {
		return bytes.HasPrefix(bytes.TrimLeft(rest, " \t\r\n"), []byte("refs/"))
	}
	if len(b) < 40 {
		return false
	}
	for _, c := range b[:40] {
		if !isHexDigit(c) {
			return false
		}
	}
	return true
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
