package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// The removal of git.worktree_remove. It runs no `git worktree remove` and no `git
// worktree prune`. The daemon deletes the worktree directory itself, then the entry of
// the worktree under <git dir>/worktrees. Every delete goes through an os.Root, so no
// delete follows a symbolic link out of the directory that holds it. That is a
// property of os.Root, not a measurement. The rules below were measured side by side
// against f6010b97 on VMs: all 107 cases equal on macOS, every remove case equal on
// Linux, and every Windows row equal except the junction rows of D19. The rows of
// 89cb6289 on which entry goes are in the tests; docs/protocol/git-worktree-remove.md has the rule. The row names are those of
// those runs. Unit tests pin this code to the rows.

// worktreeRefusal is an error of git.worktree_remove about the worktree itself. The
// reply puts "refusing to remove worktree: " before its text. Any other error gets
// "failed to remove worktree: " instead.
type worktreeRefusal struct{ text string }

func (e *worktreeRefusal) Error() string { return e.text }

func refuseWorktree(format string, a ...any) error {
	return &worktreeRefusal{text: fmt.Sprintf(format, a...)}
}

// worktreeRemoveText is the reply text for err.
func worktreeRemoveText(err error) string {
	var r *worktreeRefusal
	if errors.As(err, &r) {
		return "refusing to remove worktree: " + r.text
	}
	return "failed to remove worktree: " + err.Error()
}

// isGoneErr reports whether err says that a path, or one of its parents, is missing
// or is not a directory.
func isGoneErr(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// notADir reports whether a stat found something that is not a directory. os.Root
// reports a component of that kind with an error that is not ENOTDIR.
func notADir(fi fs.FileInfo, err error) bool {
	return err == nil && !fi.IsDir()
}

// removeTarget is the located worktree: the open directory that holds it, the name of
// the worktree in that directory, and the spelling of the worktree in the texts. The
// zero value is a worktree whose directory is not there.
type removeTarget struct {
	parent *os.Root
	leaf   string
	path   string
}

func (t removeTarget) close() {
	if t.parent != nil {
		_ = t.parent.Close()
	}
}

// locateInRepoWorktree opens the directory that holds worktreePath inside repo. The
// caller has confirmed that worktreePath is strictly inside repo and that no component
// below repo is a symbolic link. The spelling in the texts is the resolved repo joined
// with the rest of worktreePath: a remove under macOS /tmp names /private/tmp (rows
// S01_tmp and S02_tmp). A missing component, or one that is not a directory, gives the
// zero target.
func locateInRepoWorktree(repo, worktreePath string) (removeTarget, error) {
	rel, err := filepath.Rel(repo, filepath.Clean(worktreePath))
	if err != nil {
		return removeTarget{}, err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(repo))
	if err != nil {
		if isGoneErr(err) {
			return removeTarget{}, nil
		}
		return removeTarget{}, err
	}
	return openRemoveParent(resolved, filepath.Dir(rel), filepath.Base(rel))
}

// locateExternalWorktree is locateInRepoWorktree for a worktree beneath a worktreeRoot.
// The caller has confirmed that worktreePath is <worktreeRoot>/<directory>/<name>. The
// spelling in the texts is the resolved root joined with the directory and the name
// (rows S03_tmp and S04_tmp, and row B18 of the mode rows on a macOS VM).
//
// A <directory> level that is the file system root has no level above it. The root is
// then the one open level, and the spelling is "/<name>", as before the level order.
// No request reaches that case: its worktreeRoot is the file system root, and
// worktreeExternalSpellingRefusal refuses that root first.
func locateExternalWorktree(worktreePath string) (removeTarget, error) {
	cleanPath := filepath.Clean(worktreePath)
	dir := filepath.Dir(cleanPath)
	above, name := filepath.Dir(dir), filepath.Base(dir)
	if above == dir {
		name = "."
	}
	root, err := filepath.EvalSymlinks(above)
	if err != nil {
		if isGoneErr(err) {
			return removeTarget{}, nil
		}
		return removeTarget{}, err
	}
	return openRemoveParent(root, name, filepath.Base(cleanPath))
}

// externalRootDenied reports whether the worktreeRoot of worktreePath cannot be opened
// or searched. The answer of such a root comes before the symlink refusal of the
// <directory> level: with a root of mode 0300 and a symlinked <directory>, 89cb6289
// answers "open <root>: permission denied" (cell U13b on Linux and macOS VMs). Modes
// 0100, 0000 and 0200 answer the same (cells N1a to N1c on a Linux VM). With mode 0600
// the answer is "statat .: permission denied" (cell U13a on Linux and macOS VMs).
//
// Any error of the open reads as true, also for a root that is absent or is not a
// directory. That changes no answer: the lstat of the <directory> level fails for such
// a root, so the symlink refusal answers nothing there in either case.
func externalRootDenied(worktreePath string) bool {
	root, err := os.OpenRoot(filepath.Dir(filepath.Dir(filepath.Clean(worktreePath))))
	if err != nil {
		return true
	}
	defer func() { _ = root.Close() }()
	return levelSearchError(root) != nil
}

// levelSearchError is the error of a look at "." in an open level, or nil. A level
// that the daemon opened and cannot search gives "statat .: permission denied". On
// Windows it answers nil with no look: no row measures a mode there.
func levelSearchError(level *os.Root) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	_, err := level.Stat(".")
	return err
}

// openRemoveParent opens the directory that holds the worktree. On Windows a junction
// at a directory between base and the leaf makes os.Root refuse the open with "openat
// <dirRel>: path escapes from parent". Measured for a junction at .claude and at
// .claude\worktrees (rows JC and J03 to J05), and for a junction J with the dirRel
// J\sub\.claude\worktrees and J\.claude\worktrees (cells J-b-wt-pj and J-b-wt-pJ).
// The request then fails, and nothing is deleted. That refusal is divergence D19.
//
// On Linux and macOS it opens base and then each component of dirRel, one level at a
// time. After each open it looks at "." in that level, before any look at the level
// below. So a level without the read bit answers the error of its own open: "open
// <base>" or "openat <component>". A level with the read bit and without the search
// bit answers "statat .". The frames of 89cb6289 show those texts on Linux and macOS
// VMs (rows B1 to B6 and B8 to B11b with worktreeRoot, rows B17a to B17d without).
// With a root of mode 0300 or 0100 nothing is deleted there (rows B3 and B4, and row
// B18 on the macOS VM). With two
// restricted levels, the first one from the top answers (cells U7 to U10). A level
// that fails the look for another reason answers that error, and nothing is deleted
// (not measured). On Windows dirRel is opened in one step, as before.
func openRemoveParent(base, dirRel, leaf string) (removeTarget, error) {
	parent, err := os.OpenRoot(base)
	if err != nil {
		if isGoneErr(err) || notADir(os.Stat(base)) {
			return removeTarget{}, nil
		}
		return removeTarget{}, err
	}
	var levels []string
	switch {
	case dirRel == ".":
	case runtime.GOOS == "windows":
		levels = []string{dirRel}
	default:
		levels = strings.Split(dirRel, string(filepath.Separator))
	}
	for _, name := range levels {
		if err := levelSearchError(parent); err != nil {
			_ = parent.Close()
			return removeTarget{}, err
		}
		next, err := parent.OpenRoot(name)
		gone := err != nil && (isGoneErr(err) || notADir(parent.Stat(name)))
		_ = parent.Close()
		if gone {
			return removeTarget{}, nil
		}
		if err != nil {
			return removeTarget{}, err
		}
		parent = next
	}
	if err := levelSearchError(parent); err != nil {
		_ = parent.Close()
		return removeTarget{}, err
	}
	return removeTarget{parent: parent, leaf: leaf, path: filepath.Join(base, dirRel, leaf)}, nil
}

// checkLeafIdentity reports an error unless leafDir is still the directory named leaf
// in parent.
func checkLeafIdentity(parent *os.Root, leaf string, leafDir *os.Root, path string) error {
	named, err := parent.Lstat(leaf)
	if err != nil {
		return err
	}
	open, err := leafDir.Stat(".")
	if err != nil {
		return err
	}
	if !named.IsDir() || !os.SameFile(named, open) {
		return refuseWorktree("%s changed while the removal was checking it", path)
	}
	return nil
}

// worktreeGitFileTarget reads the `.git` file of the worktree through leafDir. It
// returns the git dir that the file names. A relative one is taken relative to path.
func worktreeGitFileTarget(leafDir *os.Root, path string) (string, error) {
	fi, err := leafDir.Lstat(".git")
	if errors.Is(err, fs.ErrNotExist) {
		return "", refuseWorktree("%s has no .git file", path)
	}
	if err != nil {
		return "", err
	}
	gitFile := filepath.Join(path, ".git")
	if !fi.Mode().IsRegular() {
		return "", refuseWorktree("%s is not a regular file", gitFile)
	}
	b, err := readGitPlainFileIn(leafDir, ".git")
	if err != nil {
		return "", err
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
	// Extra white space after "gitdir: " is dropped, as worktreeAdminDir drops it.
	// No row measures that input.
	rest = strings.TrimSpace(rest)
	if !ok || rest == "" {
		return "", refuseWorktree("%s does not name a git dir", gitFile)
	}
	if !filepath.IsAbs(rest) {
		rest = filepath.Join(path, rest)
	}
	return rest, nil
}

// worktreePathSet is the set of spellings that name the worktree being removed: each
// path as given and cleaned, and its pathAsRecorded form. The match is exact: a
// spelling that differs only in letter case is a different path on macOS (rows N07 and
// N08). On Windows the match ignores letter case and the slash direction, because git
// records its paths with forward slashes there (rows C01 and C02). An 8.3 short name
// does not match there (rows N83_leaf and N83_base).
type worktreePathSet []string

func newWorktreePathSet(paths ...string) worktreePathSet {
	var s worktreePathSet
	for _, p := range paths {
		if p == "" {
			continue
		}
		s = append(s, filepath.Clean(p), pathAsRecorded(p))
	}
	return s
}

func (s worktreePathSet) has(p string) bool {
	p = filepath.Clean(p)
	for _, x := range s {
		if sameCanonicalPath(x, p) {
			return true
		}
	}
	return false
}

// entryRecord is one entry under <git dir>/worktrees and the worktree path that its
// `gitdir` record names.
type entryRecord struct {
	name string
	path string
}

// loadEntryRecord reads the `gitdir` record of the entry name through root, the
// open <git dir>/worktrees. It reports false when the record cannot be read or does
// not end in `.git`.
func loadEntryRecord(root *os.Root, dir, name string) (entryRecord, bool) {
	b, err := readGitPlainFileIn(root, filepath.Join(name, "gitdir"))
	if err != nil {
		return entryRecord{}, false
	}
	rec := strings.TrimSpace(string(b))
	if rec == "" {
		return entryRecord{}, false
	}
	if !filepath.IsAbs(rec) {
		rec = filepath.Join(dir, name, rec)
	}
	rec = filepath.Clean(rec)
	if filepath.Base(rec) != ".git" {
		return entryRecord{}, false
	}
	return entryRecord{name: name, path: filepath.Dir(rec)}, true
}

// registrationsLinked reports whether the registrations folder dir is a symbolic
// link, where registrationsLinkRefused is set. A remove of a worktree folder
// that exists then deletes nothing and answers lockCheckRefusal. 5fd08069
// answers so, and the folder, its registration behind the link and its branch
// stay (cell C3 of rounds tier0-24 and tier0-25m, Linux and macOS VMs). With
// worktreeRoot it answers so too (cell L3 of round tier0-26, Linux VM). A
// create rollback with no answer of git deletes nothing there.
func registrationsLinked(dir string) bool {
	if !registrationsLinkRefused {
		return false
	}
	fi, err := os.Lstat(dir)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// openRegistrationsFolder opens the registrations folder dir with open. Where
// registrationsLinkRefused is set, a symbolic link at dir is refused, and the
// opened folder must be the folder that the Lstat saw. So no record read and no
// delete of a remove or of a create rollback goes behind such a link. The names
// of listEntryRecords are listed by the path after that test. The error then has the
// text of realDirText. claustrum's own text: no row has it in a frame.
func openRegistrationsFolder(dir string, open func(string) (*os.Root, error)) (*os.Root, error) {
	if !registrationsLinkRefused {
		return open(dir)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		// The open gives the error that the callers had before this test. A folder
		// that appeared since the Lstat is not used.
		root, openErr := open(dir)
		if openErr != nil {
			return nil, openErr
		}
		_ = root.Close()
		return nil, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, errors.New(realDirText(dir))
	}
	root, err := open(dir)
	if err != nil {
		return nil, err
	}
	if !sameOpenedFolder(root, fi) {
		_ = root.Close()
		return nil, errors.New(realDirText(dir))
	}
	return root, nil
}

// sameOpenedFolder reports whether root is the folder of fi. A folder with no
// search permission cannot be looked at through root. It passes, because no
// act can go through such a root: each fails with its own error, as in cells
// Z11a and Z11b (macOS VM, registrations folder of mode 0600).
func sameOpenedFolder(root *os.Root, fi os.FileInfo) bool {
	now, err := root.Stat(".")
	return err != nil || os.SameFile(fi, now)
}

// listEntryRecords opens <commonDir>/worktrees and reads the record of each entry. ok
// is false when the directory cannot be read. A missing directory gives no records
// and ok true. The caller closes root when it is not nil.
func listEntryRecords(commonDir string) (root *os.Root, recs []entryRecord, ok bool) {
	dir := filepath.Join(commonDir, worktreesSubdir)
	root, err := openRegistrationsFolder(dir, os.OpenRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, true
	}
	if err != nil {
		return nil, nil, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		_ = root.Close()
		return nil, nil, false
	}
	for _, e := range entries {
		if r, ok := loadEntryRecord(root, dir, e.Name()); ok {
			recs = append(recs, r)
		}
	}
	return root, recs, true
}

// entryLockPresent reports whether the entry name carries a `locked` marker. A
// dangling symbolic link counts (row L04). A marker that cannot be examined counts as
// present too. No row measures that case.
func entryLockPresent(root *os.Root, name string) bool {
	_, err := root.Lstat(filepath.Join(name, "locked"))
	return !errors.Is(err, fs.ErrNotExist)
}

// worktreeLockedByPath looks for a locked entry whose record names the worktree.
// readable is false when <commonDir>/worktrees exists but cannot be read (row L02).
// matches is the count of entries whose record names the worktree. It is not complete
// when locked is true.
func worktreeLockedByPath(commonDir string, sp worktreePathSet) (locked, readable bool, matches int) {
	root, recs, ok := listEntryRecords(commonDir)
	if !ok {
		return false, false, 0
	}
	if root == nil {
		return false, true, 0
	}
	defer func() { _ = root.Close() }()
	for _, r := range recs {
		if !sp.has(r.path) {
			continue
		}
		matches++
		if entryLockPresent(root, r.name) {
			return true, true, matches
		}
	}
	return false, true, matches
}

// registrationGitDir is the git directory whose `worktrees` directory a removal
// without worktreeRoot reads and deletes from. answered is the answer of the
// `rev-parse --absolute-git-dir` call of the removal in baseRepo, or "" when that call
// did not run.
//
// When the daemon's own environment sets GIT_COMMON_DIR, it is the directory that the
// call answered. With GIT_DIR and GIT_COMMON_DIR of another repository X in the daemon's
// environment, 89cb6289 keeps the entry of baseRepo (probe rows 1 to 4 on Linux, macOS
// and Windows VMs) and deletes an entry of X (rows 2 and 3). With GIT_DIR of X and
// GIT_COMMON_DIR of a third repository, the entry that goes is in X too (rows p3, p3b
// and p3c on Linux and macOS VMs). With GIT_COMMON_DIR alone it deletes the entry of baseRepo
// (row 5). There git itself answers the git directory of baseRepo: git's behavior, no row
// logs that answer. The same holds on Linux and macOS VMs for a submodule, a subfolder, a bare
// repository as baseRepo (rows p2a, p2b and p2c). In every other case it is verifyGitDir, as before.
//
// Not measured: an answer that is a linked-worktree entry while the daemon sets
// GIT_COMMON_DIR. claustrum keeps verifyGitDir there.
func registrationGitDir(repo, answered string) string {
	if !daemonCommonDirSet() || answered == "" {
		return verifyGitDir(repo)
	}
	answered = filepath.FromSlash(answered)
	if isWorktreeEntry(answered) || canonicalPath(answered) == canonicalPath(filepath.Join(repo, ".git")) {
		return verifyGitDir(repo)
	}
	return answered
}

// baseRepoGitFileEndsInSpace reports whether the `.git` of repo is a regular file whose
// path, as the walk of the trust check reads it (readWalkGitFile), ends in white space.
// A GIT_DIR of the daemon makes the walk skip that file, so the answer is then false.
func baseRepoGitFileEndsInSpace(repo string) bool {
	if os.Getenv("GIT_DIR") != "" {
		return false
	}
	dotGit := filepath.Join(repo, ".git")
	if fi, err := os.Stat(dotGit); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	target, ok := readWalkGitFile(dotGit, repo)
	return ok && strings.TrimSpace(target) != target
}

// lockedInBaseRepo reports whether the repository of baseRepo holds a locked entry of
// the worktree, when the removal reads its entries from another git directory
// (commonDir). It looks under <repo>/.git/worktrees: at the entry that the `.git` file
// of the worktree names, when gitDir is not "", and at each entry whose record names
// the worktree. It runs no git call. readable is false when <repo>/.git/worktrees
// exists and cannot be read. The caller then answers the lock-check refusal. 89cb6289
// answers it with no daemon environment (rows q5 and q5b, Linux VM). A directory that
// does not exist is not locked. A baseRepo with no `.git` folder is not looked at.
//
// This is divergence D22: a worktree locked in the `.git` folder of baseRepo is
// refused. 89cb6289 answers success for it in these rows, on Linux, macOS and
// Windows VMs: with GIT_DIR and GIT_COMMON_DIR of another repository in the daemon's
// environment (row p6, and row p6f with the folder gone), and with GIT_DIR alone (row
// p6e). With worktreeRoot it deletes nothing (rows q2 to q4 on a Linux VM). A
// `<repo>/.git/worktrees` that is a symbolic link counts as not readable, so the
// remove answers the lock-check refusal (openRegistrationsFolder, not measured).
func lockedInBaseRepo(repo, commonDir, gitDir, path string, sp worktreePathSet) (locked, readable bool) {
	base := filepath.Join(repo, ".git")
	if canonicalPath(base) == canonicalPath(commonDir) {
		return false, true
	}
	// A `.git` that is a file, as in a linked worktree or a submodule, holds no
	// worktrees directory.
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		return false, true
	}
	if gitDir != "" {
		if name, err := verifiedWorktreeEntry(gitDir, base, "", path, sp, nil); err == nil {
			return verifiedEntryLocked(base, name), true
		}
	}
	locked, readable, _ = worktreeLockedByPath(base, sp)
	return locked, readable
}

// baseRepoLockRefusal answers the D22 refusal of lockedInBaseRepo, or "" when the
// removal goes on. An entry that cannot be read gets the lock-check refusal, and a
// locked entry gets lockedText.
func baseRepoLockRefusal(repo, commonDir, gitDir, path, worktreePath string, sp worktreePathSet, lockedText string) string {
	locked, readable := lockedInBaseRepo(repo, commonDir, gitDir, path, sp)
	if !readable {
		return lockCheckRefusal(worktreePath)
	}
	if locked {
		return lockedText
	}
	return ""
}

// registrationProbe runs the pair of gitDirWorkTreeToplevel that 89cb6289 runs in a
// removal: `--git-dir=<gitDir> config -z --list`, then `--git-dir=<gitDir>
// --work-tree=<workTree> rev-parse --show-toplevel`, both in gitDir. gitDir is the
// answer of the `rev-parse --absolute-git-dir` call of the removal. Without
// worktreeRoot, workTree is baseRepo. Probe rows 3 and 11 to 15b show that on Linux,
// macOS and Windows VMs, row K1 on Linux, row R17a on Linux and macOS, and row D-subst
// on Windows. With worktreeRoot it is worktreePath (rows Q20b and Y8w, Linux VM). The
// zero value runs nothing.
//
// When the daemon's environment sets no GIT_COMMON_DIR, both calls carry
// GIT_COMMON_DIR=<gitDir> (row D-subst). In the state shadow (use) they carry
// GIT_COMMON_DIR=<copy> and GIT_OBJECT_DIRECTORY=<git directory>/objects. They
// have no GIT_DIR and no GIT_WORK_TREE. 5fd08069 sends them so without
// "shadowDirOff" (val-s3p2b1-linux table A6, Linux VM).
//
// With GIT_DIR and GIT_COMMON_DIR in the daemon's environment the request has a
// copy too (daemonCommonDirStays), and both calls carry the two variables of
// the copy in the place of the daemon's GIT_COMMON_DIR. On 5fd08069 the calls 3
// to 6 of a second remove have GIT_DIR=<the daemon's value>, the six profile
// values, GIT_COMMON_DIR=<S>/<R> and GIT_OBJECT_DIRECTORY=<that git
// directory>/objects, in this order (cell N-g of val-s3p2b2-8-linux2, Linux
// VM). With a daemon GIT_COMMON_DIR and no GIT_DIR the calls keep the daemon's
// value (not measured).
type registrationProbe struct {
	use      gitUse
	gitDir   string
	workTree string
}

// newRegistrationProbe makes the probe for the answer answered, or the zero value
// when the call gave no answer.
func newRegistrationProbe(u gitUse, answered, workTree string) registrationProbe {
	if answered == "" {
		return registrationProbe{}
	}
	return registrationProbe{use: u, gitDir: filepath.FromSlash(answered), workTree: workTree}
}

// run makes the two calls and returns the answer of the second, or "".
func (r registrationProbe) run() string {
	if r.gitDir == "" {
		return ""
	}
	// The pair takes the form of the off switch at its listing (not measured).
	u := r.use.atGitDirCommand()
	var pin []string
	switch {
	case u.shadow() && !daemonCommonDirStays():
		pin = u.commonVars()
	case !daemonCommonDirSet():
		pin = []string{"GIT_COMMON_DIR=" + r.gitDir}
	}
	out, err := gitDirWorkTreeToplevel(nil, r.gitDir, r.workTree, pin, u.gitDirFront())
	if err != nil {
		return ""
	}
	return out
}

// pairIfWorktreesDir runs the pair. Its first call comes before claustrum looks at the
// entries of commonDir by path. If <commonDir>/worktrees exists, the request of 89cb6289 makes the pair (probe
// rows 3 and 12, row Q20b). If it is missing, it does not (rows 1, 4, 6 and Q20). An
// empty directory gets the pair too (battery row W01 on a Windows VM). claustrum does
// not use the answer. Not measured: a worktrees directory that cannot be read.
// claustrum runs the pair there.
func (r registrationProbe) pairIfWorktreesDir(commonDir string) {
	if fi, err := os.Stat(filepath.Join(commonDir, worktreesSubdir)); err == nil && fi.IsDir() {
		r.run()
	}
}

// inRepoWorkTree is the --work-tree value of registrationProbe without worktreeRoot:
// baseRepo after filepath.EvalSymlinks. That keeps a `subst` drive (row D-subst) and a
// junction (battery row J2), and it turns an 8.3 short name into the long name (row
// D-83). All three are from a Windows VM. On Linux and macOS no row tells it from
// baseRepo as sent.
func inRepoWorkTree(repo string) string {
	if resolved, err := filepath.EvalSymlinks(filepath.Clean(repo)); err == nil {
		return resolved
	}
	return repo
}

// repeatRepositoryCheck runs the heavy listing and `rev-parse --absolute-git-dir` in
// repo once more. 89cb6289 makes these two calls when the `.git` file of the worktree
// names a git dir that is not a verified entry (probe rows 1, 3, 4, 6, 12, 13 and 14).
// claustrum does not use the answer.
func repeatRepositoryCheck(u gitUse, repo string) {
	if c := hostileConfigRefusal(u, repo, true); !c.refused() {
		noRepositoryAt(u, repo, c.listing)
	}
}

// removeListingCheck is the first listing check of a remove without worktreeRoot,
// for a worktree folder that exists. A listing that git ran and that exited
// non-zero or died by a signal runs once more, whatever its stderr says. Exit 0
// with stderr text is a success. `git version` runs before the second listing,
// except after the "not a git repository" text of exit 128. 5fd08069 does so in
// cells F1, F1n, F2 and F3 of rounds tier3-13 (macOS VM), tier3-17 (Linux VM) and
// tier3-19 (Windows VM, F1 and F2), and in cell G08b of round tier3-17.
// relisted reports that the second listing ran. final reports that a failed
// check is the answer: the text of the failed listing after the words of the lock
// check, or the lock-check refusal when it found no repository. Which listing's
// text is not measured: both gave the same text in every measured cell.
//
// noRepo is true when the trust check found no repository. Then no second
// listing runs. 5fd08069 shows it for a plain folder (cell CaP3 of round
// tier3-17, Linux VM, an empty daemon GIT_CONFIG_KEY_0). The other no-repository
// states are claustrum's own. The text of git 2.56 for a `.git` file that names
// no valid repository is not final: one more listing runs, and the answer is the
// lock-check refusal, the answer before this step was built (not measured,
// claustrum's own).
//
// The text of git 2.56 for a `.git` file that names no valid repository gets
// `git version` first (not measured). Not measured, claustrum's own: a listing
// whose folder went keeps its answer and runs no second listing, and a failed `git
// version` after the first listing is the answer, in the form of a failed second
// listing.
func removeListingCheck(u gitUse, repo string, noRepo bool) (c configCheck, relisted, final bool) {
	r := plainListing(u, repo, true)
	var ee *exec.ExitError
	if !errors.As(r.err, &ee) || isGitStop(r.err) || copyGone(r.err) {
		return listingCheck(u, r, repo, true), false, false
	}
	if noRepo {
		return listingCheck(u, r, repo, true), false, !r.saysGitFileNowhere()
	}
	if !r.saysNoRepository() {
		if v := gitVersionRun(u, true); v.err != nil {
			return configCheck{refusal: gitCannotRunPrefix + v.detail()}, false, true
		}
	}
	return hostileConfigRefusal(u, repo, true), true, true
}

// dropWorktreeEntryByPath deletes the one entry whose record names the worktree. It
// deletes nothing when no record matches, when two records match (row R03), or when a
// matching entry is locked. It reports nothing: a failure leaves the entry in place.
func dropWorktreeEntryByPath(commonDir string, sp worktreePathSet) {
	root, recs, ok := listEntryRecords(commonDir)
	if !ok || root == nil {
		return
	}
	defer func() { _ = root.Close() }()
	match := ""
	for _, r := range recs {
		if !sp.has(r.path) {
			continue
		}
		if match != "" || entryLockPresent(root, r.name) {
			return
		}
		match = r.name
	}
	if match != "" {
		_ = root.RemoveAll(match)
	}
}

// verifiedWorktreeEntry turns the git dir that the worktree's `.git` file names into
// the name of its entry under <commonDir>/worktrees. The entry must be a directory
// there, its `commondir` must lead back to commonDir, and its `gitdir` record must
// name the worktree. path is the spelling of the worktree in the texts.
//
// respell is called when the record names another path than sp holds. It returns one
// more spelling of the worktree, or "". The entry is verified when the record names
// that spelling. respell is nil when there is none. In the rows below, the call log
// of 89cb6289 shows the pair of registrationProbe. Through a `subst` drive or a
// junction the entry is deleted (Windows VM, probe row D-subst, battery rows J2, Q1
// and Q2). With a
// record of another worktree it is not (probe row 13 on Linux, macOS and Windows VMs,
// row D-83 on Windows). Not measured: a record that cannot be read. claustrum does not
// call respell there.
//
// answered is the answer of the `rev-parse --absolute-git-dir` call of the removal,
// or "". A `commondir` that leads to that directory passes too. Through a `subst`
// drive or a junction, git answers the resolved git directory, and the entry names
// that one (same Windows rows).
func verifiedWorktreeEntry(gitDir, commonDir, answered, path string, sp worktreePathSet, respell func() string) (string, error) {
	notOurs := refuseWorktree("%s carries a .git file that does not name this repository's "+
		"own worktree admin directory", path)
	g := filepath.Clean(gitDir)
	name := filepath.Base(g)
	if filepath.Base(filepath.Dir(g)) != worktreesSubdir || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) {
		return "", notOurs
	}
	dir := filepath.Join(commonDir, worktreesSubdir)
	root, err := openRegistrationsFolder(dir, os.OpenRoot)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if fi, err := root.Lstat(name); err != nil || !fi.IsDir() {
		return "", notOurs
	}
	// A `commondir` that is not a regular file is not read, so the entry is not
	// verified. With a FIFO there, 89cb6289 removes the worktree, the entry and the
	// branch (row B-G3, Linux and macOS VMs).
	b, err := readGitPlainFileIn(root, filepath.Join(name, "commondir"))
	if err != nil {
		return "", notOurs
	}
	named := strings.TrimSpace(string(b))
	if !filepath.IsAbs(named) {
		named = filepath.Join(dir, name, named)
	}
	named = filepath.Clean(named)
	if !sameCanonicalPath(named, filepath.Clean(commonDir)) &&
		!sameCanonicalPath(canonicalPathOfGone(named), canonicalPathOfGone(commonDir)) &&
		(answered == "" || !sameCanonicalPath(named, filepath.FromSlash(answered))) {
		return "", notOurs
	}
	rec, ok := loadEntryRecord(root, dir, name)
	if ok && !sp.has(rec.path) && respell != nil {
		if again := respell(); again != "" && newWorktreePathSet(again).has(rec.path) {
			return name, nil
		}
	}
	if !ok || !sp.has(rec.path) {
		return "", refuseWorktree("%s carries a .git file naming an admin directory whose "+
			"own record is of a different worktree", path)
	}
	return name, nil
}

// verifiedEntryLocked reports whether the verified entry name is locked. A worktrees
// directory that exists but cannot be opened counts as locked. Only a race reaches
// that case after the entry was verified, and no row measures it.
func verifiedEntryLocked(commonDir, name string) bool {
	root, err := openRegistrationsFolder(filepath.Join(commonDir, worktreesSubdir), os.OpenRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	defer func() { _ = root.Close() }()
	return entryLockPresent(root, name)
}

// dropWorktreeEntry deletes the verified entry name. The worktrees directory itself
// stays, also when it is then empty (rows K01 and X01).
func dropWorktreeEntry(commonDir, name string) error {
	root, err := openRegistrationsFolder(filepath.Join(commonDir, worktreesSubdir), os.OpenRoot)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.RemoveAll(name)
}

// entryAlreadyGone reports whether gitDir names an entry of the repository at commonDir
// that is already gone: the entry is missing, or the whole worktrees directory is
// (rows E03 and E04).
func entryAlreadyGone(gitDir, commonDir string) bool {
	if gitDir == "" {
		return false
	}
	g := filepath.Clean(gitDir)
	if filepath.Base(filepath.Dir(g)) != worktreesSubdir {
		return false
	}
	ownerGitDir := filepath.Dir(filepath.Dir(g))
	if !sameCanonicalPath(ownerGitDir, filepath.Clean(commonDir)) &&
		!sameCanonicalPath(ownerGitDir, canonicalPathOfGone(commonDir)) {
		return false
	}
	dir := filepath.Join(commonDir, worktreesSubdir)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return true
	}
	_, err := os.Lstat(filepath.Join(dir, filepath.Base(g)))
	return errors.Is(err, fs.ErrNotExist)
}

// deleteWorktreeDir deletes the worktree directory leaf in parent. It deletes each
// entry except `.git` first, in the order that the directory read gives, and it stops
// at the first failure. Then it deletes the rest, `.git` included, and the directory.
// On a failure `.git` stays, and so does each entry after the one that failed (rows
// P01 to P05). The error names the entry only, for example
// "RemoveAll zz: permission denied".
func deleteWorktreeDir(parent *os.Root, leaf, path string) error {
	leafDir, err := parent.OpenRoot(leaf)
	if err != nil {
		return parent.RemoveAll(leaf)
	}
	if err := checkLeafIdentity(parent, leaf, leafDir, path); err != nil {
		_ = leafDir.Close()
		return err
	}
	entries, err := readRootDir(leafDir)
	if err != nil {
		_ = leafDir.Close()
		return err
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if err := leafDir.RemoveAll(e.Name()); err != nil {
			_ = leafDir.Close()
			return err
		}
	}
	_ = leafDir.Close()
	return parent.RemoveAll(leaf)
}

// readRootDir reads the directory of root in the order the file system gives, not
// sorted.
func readRootDir(root *os.Root) ([]fs.DirEntry, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	return dir.ReadDir(-1)
}

// lockedWorktreeRefusal answers a worktree whose entry is locked.
func lockedWorktreeRefusal(worktreePath string) string {
	return "refusing to remove worktree: " + worktreePath +
		" is locked (git worktree lock); unlock it to remove it"
}
