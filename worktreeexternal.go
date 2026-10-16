package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// External worktrees (the "Worktree location" capability, added by reference build
// 7d193f89): git.worktree_create / git.worktree_remove accept a worktreeRoot, and
// the session folder is then created OUTSIDE the repository, beneath that root, at
// exactly <worktreeRoot>/<directory>/<name> — two components below the root. When
// worktreeRoot is set the in-repo containment (worktreePathRefusal) is replaced by
// the checks here. Every message and the marker bytes below were measured
// byte-for-byte against 7d193f89 on an ephemeral VM.

// worktreeExternalSpellingRefusal and worktreeExternalShapeRefusal report the
// reference's refusal when an external worktreeRoot/worktreePath pair is not a valid
// location, or "" if it is. verb is "create" or "remove". Checks run in the
// reference's measured order:
//
//  1. worktreeRoot must be absolute and carry no ".." component — its refusals name
//     the root and use the "worktree location … beneath the filesystem root" wording;
//  2. worktreePath must be absolute (an empty path reads as relative) and carry no
//     ".." — same wording as the in-repo containment ("session folder");
//  3. worktreePath must be exactly <worktreeRoot>/<directory>/<name>.
//
// The ".." checks run on the raw path before any filepath.Clean, so a path that
// would clean back to a valid location is still refused — matching 7d193f89.
// worktreeExternalSpellingRefusal is steps 1 and 2. Both worktree methods check
// baseRepo between steps 2 and 3. On git.worktree_create that order is not measured.
//
// claustrum also refuses, after step 2, a worktreeRoot that is the file system root.
// For a root of "/" and a worktreePath of "/<name>", 89cb6289 sends the "is a filesystem
// root" text. The create sends it after 3 git calls, and the remove with no git call.
// That holds with the folder present or gone, and for a daemon that runs as root (Linux
// VM, cells R1n, R1u and R1r). On a macOS VM the create is cell R1a, and the remove is
// cells R1a and R1b. The macOS cells have the folder gone and a normal user. Nothing
// changes on disk. The calls place it before `rev-parse
// --show-toplevel`. Not measured: its order against the baseRepo test and step 3. A
// root with a ".." component that cleans to "/" gets the ".." text (from the code, not
// measured).
//
// More cells ran against 89cb6289 on Linux and macOS VMs. A root of "//" or "/." gets
// the same text, with the root as sent (cells R2 and R3). So does a worktreePath of
// "/<directory>/<name>" under a root of "/" (cell R6). A mount point that is not "/"
// is not refused: the create and the remove succeed (cell R4).
//
// On 5fd08069 a relative worktreePath wins over that text: a root of "/" with the
// path "x/y" gets the relative-path text (cell C-W1 of round tier0-20, Linux VM). A
// root with a component named .git or .claude-managed-worktrees, in any letter
// case, is refused with the name as sent. The cells measure .git and .GIT (cells
// C-W4, C-W4e and C-S2). A component .claude-managed-worktrees is refused the same
// way (not measured). Its order against the worktreePath test is not measured.
func worktreeExternalSpellingRefusal(worktreeRoot, worktreePath, verb string) string {
	if !filepath.IsAbs(worktreeRoot) {
		return fmt.Sprintf("refusing to %s worktree: %s is a relative path; choose the "+
			"worktree location by its absolute path, without %q, beneath the filesystem root",
			verb, worktreeRoot, "..")
	}
	if pathHasDotDot(worktreeRoot) {
		return fmt.Sprintf("refusing to %s worktree: %s contains a %q component; choose the "+
			"worktree location by its absolute path, without %q, beneath the filesystem root",
			verb, worktreeRoot, "..", "..")
	}
	for _, name := range strings.Split(filepath.ToSlash(worktreeRoot), "/") {
		if reservedLocationName(name) {
			return fmt.Sprintf("refusing to %s worktree: %s has a component named %s, which is "+
				"reserved; choose the worktree location by its absolute path, without %q, beneath "+
				"the filesystem root", verb, worktreeRoot, name, "..")
		}
	}
	if msg := sessionFolderSpellingRefusal(worktreePath, verb); msg != "" {
		return msg
	}
	if isFilesystemRoot(worktreeRoot) {
		return fmt.Sprintf("refusing to %s worktree: %s is a filesystem root; choose the "+
			"worktree location by its absolute path, without %q, beneath the filesystem root",
			verb, worktreeRoot, "..")
	}
	return ""
}

// reservedLocationName reports whether name is .git or .claude-managed-worktrees, in
// any letter case. A name with trailing dots or blanks is not measured, and passes.
func reservedLocationName(name string) bool {
	return strings.EqualFold(name, ".git") || strings.EqualFold(name, managedWorktreesMarker)
}

// worktreeExternalShapeRefusal is step 3 of the checks listed above
// worktreeExternalSpellingRefusal. Callers run worktreeExternalSpellingRefusal first,
// so worktreePath is absolute and has no ".." component here. The text quotes both
// members as sent, with a "/./" or a final "/" kept (5fd08069, cell C-W2 of round
// tier0-20, Linux VM).
//
// Then the <directory> or the <name> of the path is refused when it is a reserved
// name, with the name as sent. The cells put it at the <directory> (cells C-W3 and
// C-S1). A reserved <name>, and the <directory> tested first, are not measured.
func worktreeExternalShapeRefusal(worktreeRoot, worktreePath, verb string) string {
	root := filepath.Clean(worktreeRoot)
	wp := filepath.Clean(worktreePath)
	if filepath.Dir(filepath.Dir(wp)) != root {
		return fmt.Sprintf("refusing to %s worktree: %s is not "+
			"<worktree location>/<directory>/<name> beneath %s", verb, worktreePath, worktreeRoot)
	}
	for _, name := range []string{filepath.Base(filepath.Dir(wp)), filepath.Base(wp)} {
		if reservedLocationName(name) {
			return fmt.Sprintf("refusing to %s worktree: %s uses the reserved name %s for a "+
				"directory under the worktree location", verb, worktreePath, name)
		}
	}
	return ""
}

// repoFolderHoldsRepoRefusal refuses a <directory> level that is the repository or
// holds it, or answers "". The text names worktreeRoot, the <directory> level as the
// path spells it, and baseRepo as sent. Without resolve the test compares the
// cleaned paths. With resolve it compares them with their links followed. 5fd08069
// refuses so with no git call in the remove (cells C-W5 and C-S3 of round tier0-20,
// Linux VM). With a baseRepo through a link the create refuses after `worktree list`
// (cell C-W6). The remove refuses there too, after `worktree list` (cell C-S8).
func repoFolderHoldsRepoRefusal(worktreeRoot, worktreePath, baseRepo, repo, verb string, resolve bool) string {
	folder := filepath.Dir(filepath.Clean(worktreePath))
	f, r := folder, filepath.Clean(repo)
	if resolve {
		f, r = canonicalPathOfGone(folder), canonicalPathOfGone(repo)
	}
	if !samePath(r, f) && !pathStrictlyUnder(r, f) {
		return ""
	}
	return fmt.Sprintf("refusing to %s worktree: %s would place worktrees in %s, which contains the "+
		"repository %s; choose a location the repository is not inside", verb, worktreeRoot, folder, baseRepo)
}

// repoFolderInWorktreeRefusal refuses a <directory> level that is a listed worktree
// of the repository or lies inside one, or answers "". The test compares the cleaned
// path with each listed path as git lists it. 5fd08069 refuses so (cells C-W19 and
// C-S7 of round tier0-20, Linux VM). A bare entry is skipped, and so is a listed
// folder that is gone. A remove then answers that the root is not reachable, as on
// 89cb6289 (rows Y9 and T3). The main checkout counts (not measured).
func repoFolderInWorktreeRefusal(worktreeRoot, worktreePath, baseRepo string, listed []listedWorktree, verb string) string {
	folder := filepath.Dir(filepath.Clean(worktreePath))
	for _, w := range listed {
		if w.bare || !samePath(folder, w.path) && !pathStrictlyUnder(folder, w.path) {
			continue
		}
		if _, err := os.Lstat(w.path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		return fmt.Sprintf("refusing to %s worktree: %s would place worktrees in %s, which is (or is "+
			"inside) %s, a worktree of the repository %s; a worktree location must be outside the "+
			"repository's checkouts", verb, worktreeRoot, folder, w.path, baseRepo)
	}
	return ""
}

// worktreeRootInRepoRefusal is the refusal of a worktreeRoot that is baseRepo or lies
// beneath it, or "" when the root passes. The test cleans both paths and compares
// whole components. It resolves no symlink and runs no git. The text names
// worktreeRoot and baseRepo as sent. verb is "create" or "remove". f6010b97 and
// 89cb6289 send the same frames on Linux and macOS VMs:
//   - Refused: the repository T, T/.claude, T/.claude/ with its slash kept in the
//     text, T/.claude/worktrees, T/sub and a missing T/missing. These are Linux rows
//     Q1, Q7, Q8, Q8b, Q2s to Q5s and K1 to K3, and macOS rows MX3, MX4, MX11, MX13,
//     MX14 and MX15.
//   - Passed: a root that holds the repository, and B/Tx beside B/T (Linux rows
//     Q15s, K6 and K7).
func worktreeRootInRepoRefusal(worktreeRoot, baseRepo, verb string) string {
	if !samePath(worktreeRoot, baseRepo) && !pathStrictlyUnder(worktreeRoot, baseRepo) {
		return ""
	}
	return fmt.Sprintf("refusing to %s worktree: %s is the repository %s or inside it; "+
		"a worktree location must be outside the repository", verb, worktreeRoot, baseRepo)
}

// listedWorktree is one entry of `git worktree list --porcelain`, with or without -z.
// It holds the
// path as git lists it, and whether the entry has a "bare" line.
type listedWorktree struct {
	path string
	bare bool
}

// d5Kill is err when the D5 deadline (gitTimeout) of ctx stopped the failed git call,
// and nil otherwise. err is then the exec error, for example "signal: killed".
func d5Kill(ctx context.Context, err error) error {
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return err
	}
	return nil
}

// worktreeListTwice runs worktreeListCall with -z. After a failure that the D5
// deadline did not cause, it runs the call once more without -z. It answers the
// entries in list order, whether the D5 deadline stopped the first call, and the
// error of the last call. git lists the main checkout first. 5fd08069 makes the
// second call in a create (cell C-W17 of round tier0-20, Linux VM) and 89cb6289 in a
// remove (row DG2s-g, Linux VM).
func worktreeListTwice(u gitUse, repo string) (listed []listedWorktree, killed bool, err error) {
	listed, killed, err = worktreeListCall(u, repo, true)
	if err == nil || killed {
		return listed, killed, err
	}
	listed, _, err = worktreeListCall(u, repo, false)
	return listed, false, err
}

// worktreeListCall runs a light `git worktree list --porcelain` in repo, with its
// listing. With nul it adds -z, and a NUL byte ends each line of the output. Without
// nul a LF ends each line. It returns the entries, whether the D5 deadline stopped the
// call, and its exec error.
func worktreeListCall(u gitUse, repo string, nul bool) (list []listedWorktree, killed bool, err error) {
	ctx, cancel := gitCtx()
	defer cancel()
	args, sep := []string{"worktree", "list", "--porcelain"}, "\n"
	if nul {
		args, sep = append(args, "-z"), "\x00"
	}
	b, err := hardenedGitCmd(ctx, u, repo, false, nil, args...).Output()
	if err != nil {
		return nil, d5Kill(ctx, err) != nil, err
	}
	out := strings.TrimRight(string(b), "\n")
	for _, line := range strings.Split(out, sep) {
		switch {
		case strings.HasPrefix(line, "worktree "):
			list = append(list, listedWorktree{path: strings.TrimPrefix(line, "worktree ")})
		case len(list) == 0:
		case line == "bare":
			list[len(list)-1].bare = true
		}
	}
	return list, false, nil
}

// parentStepPrefix starts every failure of the parent step of git.worktree_create.
const parentStepPrefix = "failed to create parent directory: "

// worktreeListRefusalPrefix starts the refusal of git.worktree_remove with
// worktreeRoot when git cannot list the worktrees of baseRepo.
const worktreeListRefusalPrefix = "failed to remove worktree: cannot list the repository's worktrees: "

// worktreeListForRemove is the `worktree list` step of git.worktree_remove with
// worktreeRoot. It answers the entries, or a refusal, and then nothing is deleted.
//
// In row DG2s-g (Linux VM), `worktree list --porcelain -z` exits 128 on 89cb6289. It
// then runs one more listing and `worktree list --porcelain` with no -z. That call
// exits 128 too, and the answer is worktreeListRefusalPrefix with that exec error. The
// worktree and its entry stay. The repository of that row has a valid HEAD, and its
// `.git/commondir` is a dangling relative symlink. claustrum takes the same steps
// after every failure of the first call that is not a D5 stop.
//
// If the D5 deadline stops the first call, the answer is the work-tree refusal with
// the exec error, as before. That is claustrum's choice (not measured). Not measured
// either: a second call that succeeds. claustrum then reads its output line by line
// and goes on. A path with a newline in it is not measured in that form. A git older
// than 2.36, which has no `worktree list -z`, takes that path.
//
// A baseRepo that does not exist runs no call here, and the removal goes on. In rows
// L13wa-g and L13wb-g (Linux VM) the excludes read is the only git call, on 89cb6289
// and on claustrum. The later git calls of claustrum for that baseRepo do not start:
// the working directory does not exist.
func worktreeListForRemove(u gitUse, repo string) (listed []listedWorktree, refusal string) {
	if _, err := os.Stat(repo); errors.Is(err, fs.ErrNotExist) {
		return nil, ""
	}
	listed, killed, err := worktreeListTwice(u, repo)
	switch {
	case killed:
		return nil, workTreeUnknownPrefix + err.Error()
	case err != nil:
		return nil, worktreeListRefusalPrefix + err.Error()
	}
	return listed, ""
}

// repoCheckouts holds what the location tests compare a folder with: the folders of
// the repository and its listed worktrees. baseRepo is the text of the request.
type repoCheckouts struct {
	baseRepo string
	folders  []os.FileInfo
	listed   []listedWorktree
}

// newRepoCheckouts reads the folders of the repository at repo. They are topLevel,
// the answer of `rev-parse --show-toplevel`, the main checkout that git lists first,
// and the git directory that the trust check pinned (pruneGitDir). A folder that
// cannot be read is left out. A bare first entry has no main checkout, so it is not
// compared. That is claustrum's choice (not measured).
func newRepoCheckouts(repo, baseRepo, topLevel string, listed []listedWorktree) repoCheckouts {
	c := repoCheckouts{baseRepo: baseRepo, listed: listed}
	paths := []string{topLevel}
	if len(listed) > 0 && !listed[0].bare {
		paths = append(paths, listed[0].path)
	}
	paths = append(paths, pruneGitDir(repo))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil {
			c.folders = append(c.folders, fi)
		}
	}
	return c
}

// refusalAt is the refusal of a worktreeRoot that passes through the folder at, or
// "". The repository form comes first: at is the same file (device and inode) as one
// of the folders of the repository. Then the linked form: at is, as a string, the
// path of a listed worktree other than the first. A listed path that does not exist
// counts only with keepGone, and gone then holds that path and the identity of its
// parent at this test. verb is "create" or "remove".
func (c repoCheckouts) refusalAt(worktreeRoot, at, verb string, keepGone bool) (msg string, gone goneWorktree) {
	if fi, err := os.Stat(at); err == nil {
		for _, f := range c.folders {
			if os.SameFile(fi, f) {
				return fmt.Sprintf("refusing to %s worktree: %s leads into the repository %s (at %s); "+
					"a worktree location must be outside the repository", verb, worktreeRoot, c.baseRepo, at), goneWorktree{}
			}
		}
	}
	for i, w := range c.listed {
		if i == 0 || !samePath(at, w.path) {
			continue
		}
		if _, err := os.Lstat(w.path); errors.Is(err, fs.ErrNotExist) {
			if !keepGone {
				continue
			}
			gone.path = w.path
			if goneIdentityStep != nil {
				goneIdentityStep()
			}
			// Lstat, so a parent that is a link here never matches the root that
			// makeGoneWorktreeFolder opens.
			gone.parent, _ = os.Lstat(filepath.Dir(w.path))
		}
		return fmt.Sprintf("refusing to %s worktree: %s leads into %s, a worktree of the repository %s (at %s); "+
			"a worktree location must be outside the repository's checkouts", verb, worktreeRoot, w.path, c.baseRepo, w.path), gone
	}
	return "", goneWorktree{}
}

// goneWorktree is the folder of a listed worktree that is gone, and the identity of
// its parent when the checkout test ran. The zero value is no such folder.
type goneWorktree struct {
	path   string
	parent os.FileInfo
}

// goneIdentityStep runs right before refusalAt takes the identity of the parent, and
// goneFolderStep right before makeGoneWorktreeFolder opens the parent. Tests set them
// to change the parent after the checkout test.
var goneIdentityStep, goneFolderStep func()

// makeGoneWorktreeFolder makes the folder of g with mode 0700, through a root at its
// parent. The parent must still be the folder that the checkout test saw, by
// identity. The identity comes from an lstat of the parent, so a parent that is a
// link at that moment or later leads nowhere, and nothing is made. Two windows are
// not covered. A real folder put at the parent between the owner and mode tests of
// the ancestor walk and that lstat gets the folder with no owner or mode test. A
// folder above the parent that becomes a link between the resolve of the checkout
// test and that lstat leads the folder behind the link. Each makes one empty 0700
// folder and deletes nothing. A failure makes nothing and is not answered (not
// measured).
func makeGoneWorktreeFolder(g goneWorktree) {
	if goneFolderStep != nil {
		goneFolderStep()
	}
	if g.path == "" || g.parent == nil {
		return
	}
	root, err := os.OpenRoot(filepath.Dir(g.path))
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	if fi, err := root.Stat("."); err != nil || !os.SameFile(fi, g.parent) {
		return
	}
	_ = root.Mkdir(filepath.Base(g.path), 0o700)
}

// worktreeRootCheckoutRefusal is the refusal of a worktreeRoot that passes
// worktreeRootInRepoRefusal but leads into a checkout of the repository, or "" when
// the root passes. It resolves the root as canonicalPathOfGone does and tests each
// folder of that path from "/" down with refusalAt. The first folder that fails
// decides. The text names that folder, and worktreeRoot and baseRepo as sent.
// f6010b97 and 89cb6289 send the same frames on Linux and macOS VMs:
//   - A symlinked root, or one under a symlinked prefix that does not exist (Linux
//     rows Q6s, Q17, Q18, K4, K8 and E1, macOS rows MX12 and E1).
//   - A baseRepo below the top level, such as T/sub (Linux rows Q19, K9 and E2).
//   - A linked worktree W as baseRepo with the root in the main checkout T. The text
//     names T (rows W1, W3, Y6 and Y11c). The root in W itself names W (row Y4).
//   - A path that is the same file under another spelling: a bind mount (Linux row
//     Y8), a firmlink (macOS row Y8m) and a case variant (macOS row E8). macOS /tmp
//     gives "/private/tmp" (row E3). With baseRepo in a linked worktree, a bind
//     mount (Linux row T1) or a case variant (macOS row T1m) of the main checkout
//     matches too. The main checkout is the outer folder in row T2b.
//   - The linked form: rows W2, W4, Y1, Y2c, Y5, Y11a and Y10. Of two nested
//     worktrees, the outer one is named (row Y6n). It passes w0 listed through a
//     symlinked spelling (row Y2), a case variant of w0 (macOS row Y3), a bind mount
//     of w0 (Linux row Y8w) and a root that holds w0 (row Y7).
//
// 5fd08069 refuses a root in a separate git directory of the repository with the
// repository form (cells C-W7 and C-S4 of round tier0-20, Linux VM). It names a
// linked worktree that holds the main checkout before that checkout (cell C-W21).
// In a create it refuses a root in a listed worktree whose folder is gone, and it
// makes that folder first (cell C-W8). keepGone is true for a create, and gone is
// then the folder to make (makeGoneWorktreeFolder). In a remove such a worktree
// passes, as on 89cb6289 (rows Y9 and T3). Any other error of the lstat keeps the
// entry (not measured).
func worktreeRootCheckoutRefusal(worktreeRoot string, c repoCheckouts, verb string, keepGone bool) (msg string, gone goneWorktree) {
	var chain []string
	for at := canonicalPathOfGone(worktreeRoot); ; at = filepath.Dir(at) {
		chain = append([]string{at}, chain...)
		if filepath.Dir(at) == at {
			break
		}
	}
	for _, at := range chain {
		if msg, gone := c.refusalAt(worktreeRoot, at, verb, keepGone); msg != "" {
			return msg, gone
		}
	}
	return "", goneWorktree{}
}

// worktreeRootMissingRefusal is the refusal of git.worktree_remove for a
// worktreeRoot that cannot be reached, or "" when it resolves. It comes after
// worktreeRootCheckoutRefusal, before any later step of the removal, and nothing is
// deleted. The first slot is worktreeRoot as sent. The reason:
//   - A root with missing folders gets "<path> does not exist". 89cb6289 and
//     f6010b97 send that for a deleted linked worktree (rows Y9 and T3) and for two
//     missing levels (row T4). 5fd08069 follows the links of the existing prefix and
//     drops a final slash (cells C-W22 and X3 on Linux VMs, M1 and M2 on a macOS VM,
//     rounds tier0-20 and tier0-22).
//   - A prefix that is not a folder: "<prefix>: not a directory" (5fd08069, cell
//     C-W24). A root that is itself a file is not measured, and goes on.
//   - A symlink that does not resolve gives the error of filepath.EvalSymlinks, for
//     example "lstat <target>: no such file or directory" (row T5).
//
// Any other error of the lstat keeps the old answer. For "permission denied" that
// answer has the frame of the references (Linux row T6).
func worktreeRootMissingRefusal(worktreeRoot string) string {
	prefix := filepath.Clean(worktreeRoot)
	var missing []string
	for {
		_, err := os.Lstat(prefix)
		if err == nil {
			break
		}
		up := filepath.Dir(prefix)
		if !isGoneErr(err) || up == prefix {
			return ""
		}
		missing = append([]string{filepath.Base(prefix)}, missing...)
		prefix = up
	}
	var reason string
	resolved, err := filepath.EvalSymlinks(prefix)
	switch {
	case err != nil:
		reason = err.Error()
	case len(missing) == 0:
		return ""
	case notADir(os.Stat(resolved)):
		reason = resolved + ": not a directory"
	default:
		reason = filepath.Join(append([]string{resolved}, missing...)...) + " does not exist"
	}
	return fmt.Sprintf("failed to remove worktree: the worktree location %s is not reachable (%s); "+
		"nothing was removed — retry once it is available, or remove the worktree by hand", worktreeRoot, reason)
}

// repoTopLevel is the answer of a light `rev-parse --show-toplevel` in repo, or ""
// when the call fails. pre is nil when the call gets its own listing, else the
// listing it pins (see hardenedGitCmd). The error is d5Kill of the call.
func repoTopLevel(u gitUse, repo string, pre *configListing) (string, error) {
	top, killed, err := repoTopLevelCall(u, repo, pre)
	if killed {
		return "", err
	}
	return top, nil
}

// repoTopLevelCall is repoTopLevel with the exec error of a failed call, and
// whether the D5 deadline stopped it.
func repoTopLevelCall(u gitUse, repo string, pre *configListing) (top string, killed bool, err error) {
	ctx, cancel := gitCtx()
	defer cancel()
	b, err := hardenedGitCmd(ctx, u, repo, false, pre, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", d5Kill(ctx, err) != nil, err
	}
	return strings.TrimRight(string(b), "\n"), false, nil
}

// sessionLocationRefusal is the refusal of a create whose worktreeRoot lies in a
// session worktree, or "". It resolves the root as canonicalPathOfGone does and
// tests each folder of that path from "/" down. The first folder below a folder
// with a .claude-managed-worktrees file, or below .claude/worktrees, decides. A
// folder that exists gets the "leads into" text, and a missing one the "would be
// made" text. Then a root that holds that file itself is refused. 5fd08069 refuses
// so (cells C-W9, C-W10 and C-W11 of round tier0-20, Linux VM). A .claude/worktrees
// folder above the root is not measured.
func sessionLocationRefusal(worktreeRoot string) string {
	const tail = "; beneath .claude/worktrees, or beneath a directory holding a .claude-managed-worktrees " +
		"marker); a worktree location must be outside every session's worktree"
	resolved := canonicalPathOfGone(worktreeRoot)
	var chain []string
	for at := resolved; ; at = filepath.Dir(at) {
		chain = append([]string{at}, chain...)
		if filepath.Dir(at) == at {
			break
		}
	}
	for _, at := range chain {
		if !belowSessionFolder(at) {
			continue
		}
		if _, err := os.Lstat(at); err == nil {
			return fmt.Sprintf("refusing to create worktree: %s leads into a session worktree (at %s%s", worktreeRoot, at, tail)
		}
		return fmt.Sprintf("refusing to create worktree: %s would be made inside a session worktree or its "+
			"per-repository directory (at %s%s", worktreeRoot, at, tail)
	}
	if holdsManagedMarker(resolved) {
		return fmt.Sprintf("refusing to create worktree: %s is itself a per-repository worktree directory "+
			"(it holds a .claude-managed-worktrees marker); choose the directory above it, or another location", worktreeRoot)
	}
	return ""
}

// belowSessionFolder reports whether a folder above p holds a
// .claude-managed-worktrees file, or is a worktrees folder in a .claude folder.
func belowSessionFolder(p string) bool {
	for at := filepath.Dir(p); ; at = filepath.Dir(at) {
		if holdsManagedMarker(at) || filepath.Base(at) == worktreesSubdir && filepath.Base(filepath.Dir(at)) == claudeDirName {
			return true
		}
		if filepath.Dir(at) == at {
			return false
		}
	}
}

// holdsManagedMarker reports whether dir holds a .claude-managed-worktrees entry that
// is not a folder.
func holdsManagedMarker(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, managedWorktreesMarker))
	return err == nil && !fi.IsDir()
}

// worktreeExternalDirSymlinkRefusal reports 7d193f89's refusal when the <directory>
// level (filepath.Dir(worktreePath)) is a symbolic link, or "" if it is a real
// directory (or absent). The worktreeRoot itself MAY be a symlink — only the
// per-repository directory beneath it must be real, so a repo cannot plant a link
// that carries the create out of the chosen location. verb is "create" or "remove"
// (create carries errorCode unsafe_path; remove carries none). Measured against
// 7d193f89: this runs after the ownership/writability checks on create, and before
// the registration checks on remove. The <directory> comes
// from the cleaned path, so a worktreePath that ends in a slash names its real
// parent (see externalWorktreeDirNotEmptyRefusal).
func worktreeExternalDirSymlinkRefusal(worktreePath, verb string) string {
	dir := filepath.Dir(filepath.Clean(worktreePath))
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Sprintf("refusing to %s worktree: %s is a symbolic link; the directory "+
			"under the worktree location must be a real directory", verb, dir)
	}
	return ""
}

// verifyGitDir is the git directory whose worktrees directory git.worktree_remove
// reads. It is the repository git directory that the trust check pinned for baseRepo
// (pruneGitDir). For a linked worktree used as baseRepo, that is the git directory of
// the main repository. Its own `.git` is a file, so <baseRepo>/.git/worktrees never
// exists. When the pin is <baseRepo>/.git itself, the path keeps the spelling of
// baseRepo as given, so the transient text does not change.
//
// f6010b97 reads the worktrees directory of that repository git directory. Measured
// on a Linux VM with worktreeRoot set:
//   - A linked worktree as baseRepo removes a worktree that the main repository
//     registered. The worktree, its entry and its branch go, and the answer is
//     {"success":true} (rows WR07 and K08).
//   - The same holds when the main repository carries a stray commondir (row WR14).
//     The check judges the git directory of baseRepo, which is the entry of the
//     linked worktree. The main repository's git directory is only the pin, and
//     the check does not judge it again.
//   - A subdirectory of a repository and a submodule as baseRepo also remove a
//     registered worktree (rows K02 and K09).
//   - With a daemon GIT_DIR that names another repository X, the transient text
//     names X/.git/worktrees (row K15).
func verifyGitDir(baseRepo string) string {
	asGiven := filepath.Join(baseRepo, ".git")
	g := pruneGitDir(baseRepo)
	if canonicalPath(g) == canonicalPath(asGiven) {
		return asGiven
	}
	return g
}

// workTreeUnknownPrefix starts every refusal of git.worktree_remove with worktreeRoot
// that comes before the registration check. See externalWorkTreeRefusal.
const workTreeUnknownPrefix = "failed to remove worktree: cannot determine the repository's work tree: "

// externalWorkTreeRefusal is the check of git.worktree_remove with worktreeRoot that
// comes after the containment checks and before the dir-symlink check. It answers ""
// when the removal goes on. Otherwise it answers workTreeUnknownPrefix and a reason,
// and nothing is deleted. It runs before the "already gone" test, so a missing
// worktree gets the same answer. The reasons, in order:
//
//  1. Git cannot start in baseRepo (unenterableBaseListing). The reason is the hooks
//     refusal with the start error (rows K10, K11 and K14).
//  2. The trust check refuses the git directory of baseRepo. The reason is the
//     trust refusal text (rows WR01 to WR06, and WR08 with a plain folder).
//  3. The trust check finds no repository. The reason is "exit status 128" (rows
//     WR10 to WR13, K03 and K05). On Linux and macOS a listing that fails there
//     gives its refusal text as the reason (89cb6289, rows A-N5b and A-N5c). A `.git`
//     file with a gone target that is not an entry goes on to reason 4 (row K16).
//  4. The configuration cannot be listed. The reason is the refusal text of
//     hostileConfigRefusal (rows WR15, K06 and K16). When the listing says git finds
//     no repository, the reason is "git finds no repository here: " and git's text
//     (89cb6289, row N04 on Linux and macOS VMs).
//  5. baseRepo is itself a git directory. The reason is "exit status 128" (rows
//     K07 and K25). A work tree whose own repository has core.bare set still passes
//     (row K17).
//  6. The hardened `git rev-parse --absolute-git-dir` fails in baseRepo. The reason
//     is the exec error, for example "exit status 128" (rows K12, K18 and K21).
//
// A baseRepo that does not exist skips every reason, and no git call runs here. That
// holds with git on PATH and without it (89cb6289, rows L13wa, L13wb, L13wa-g and
// L13wb-g on a Linux VM). The removal then goes on. A baseRepo that fails os.Stat
// with any other error keeps every reason. Only the GIT_CONFIG_COUNT check does not
// run for it. Every other row, and the order
// against the containment and dir-symlink checks, was measured side by side against
// f6010b97 on a Linux VM.
//
// Before these reasons comes the check of the daemon's own GIT_CONFIG_COUNT, for a
// baseRepo that os.Stat reads. When it refuses, the reason is the trust refusal text if the
// trust check refuses, and else the count refusal. See docs/PROTOCOL.md, "The
// daemon's own git environment".
//
// Then, with no git on PATH, the reason is the exec error alone: `exec: "git":
// executable file not found in $PATH`. It comes before the six reasons, so it also
// answers a git directory that the trust check refuses. 89cb6289 and f6010b97 answer
// so on a Linux VM in these rows:
//   - a repository, with the worktree folder present and gone (L13xc and L13xd)
//   - a regular file (L13u) and a repository folder of mode 0600 (L13v)
//   - a `.git` file to nowhere (L13y) and a plain folder (L13z)
//   - a git directory with a stray commondir that reads "x" (L13t)
//   - a chain of 45 symlinks to a repository (LNKb and LNKr)
//
// Not measured with no git: the place of the GIT_CONFIG_COUNT check (claustrum keeps
// it first when os.Stat reads baseRepo), other commondir contents, and the other
// shapes of reason 3.
//
// After reason 3 and before the listing of reason 4 comes the daemon's own read of
// the repository's config (repoConfigGate). Its refusal text is the reason.
// 5fd08069 answers so with format version `2` and with a config of 20 MiB, and
// removes nothing (cells N14, Linux and macOS VMs). Each state ran for a worktree
// that exists and for a folder that is missing. An info/exclude of 20 MiB and a
// syntax error in the config get the same frame (cells P4, macOS VM). The place
// of the read among the reasons is not measured: those cells have a repository
// that passes reasons 1 to 3. off is the `shadowDirOff` member of the request.
// With it and a config of 20 MiB, 5fd08069 removes the worktree (cell P4, macOS
// VM).
//
// Its second answer is the git top level of baseRepo when the removal goes on, or ""
// when git cannot give it. worktreeRootCheckoutRefusal uses it.
//
// That read is a part of the settings gate of the request (settingsGateRefusal).
// After the gate the calls of the request name the copy of the repository in the
// state shadow. On 5fd08069 the 8 calls of a remove with worktreeRoot carry the
// four folder variables there, and 14 with branchName (cell N3 of
// val-s3p2b2-3-linux2, Linux VM). The calls before the gate have no copy. A
// failed start test in a remove with worktreeRoot is not measured: its text is
// the reason, as for a refused config.
func externalWorkTreeRefusal(req *request, repo string, off jsonTrueSwitch) (string, string) {
	u := gitUse{}
	// Only "does not exist" skips the reasons. Any other stat error keeps them. A
	// baseRepo behind a symlink chain that the kernel does not follow still resolves in
	// the trust check. If the reasons are skipped for it, the removal deletes its
	// worktree.
	_, statErr := os.Stat(repo)
	if errors.Is(statErr, fs.ErrNotExist) {
		return "", ""
	}
	if statErr == nil {
		if msg, bad := daemonCountRefusal(); bad {
			if t := requestGitDirTrust(repo, false); t.verdict == gitDirRefused {
				msg = t.refusal
			}
			return workTreeUnknownPrefix + msg, ""
		}
	}
	if err := gitLookupError(); err != nil {
		return workTreeUnknownPrefix + err.Error(), ""
	}
	if msg := unenterableBaseListing(repo); msg != "" {
		return workTreeUnknownPrefix + msg, ""
	}
	t := requestGitDirTrust(repo, false)
	switch t.verdict {
	case gitDirRefused:
		return workTreeUnknownPrefix + t.refusal, ""
	case gitDirNoRepo:
		if !goneNonEntryGitFile(repo) {
			// The call log of 89cb6289 shows a light listing and `rev-parse
			// --show-toplevel` here, both with GIT_DIR=<null device>. The rev-parse
			// exits 128 (rows L13z-g and DG2h-g on a Linux VM). claustrum makes the two
			// calls and does not read the answer of the rev-parse. A listing that fails
			// gives its refusal text as the reason (rows A-N5b and A-N5c on Linux and
			// macOS VMs, row A-N5 on a Linux VM). Windows refuses a worktreeRoot before this check. Without the
			// entry of t.noRepoPinned the reason stays "exit status 128", as before. A
			// daemon GIT_DIR that names a regular file is such a state. Not measured here.
			// git 2.56 fails the listing of a gone entry with its gitfile text. 5fd08069
			// answers "exit status 128" there, as with older gits (cell WR13 of round
			// tier0-27, Linux VM).
			c := hostileConfigRefusal(u, repo, false)
			if c.refusal != "" && t.noRepoPinned() && c.gitFileText == "" {
				return workTreeUnknownPrefix + c.refusal, ""
			}
			if !c.refused() {
				_, _ = repoTopLevel(u, repo, &c.listing)
			}
			return workTreeUnknownPrefix + "exit status 128", ""
		}
	}
	// The daemon's own read of the repository's config (cells N14, Linux and macOS
	// VMs). The
	// call that it makes to git on a refusal is the light `rev-parse
	// --show-toplevel` that follows the listing below.
	// A bad answer of git about the repository is not measured with worktreeRoot.
	// Its text is the reason, and git's own refusal gets the reason of a folder
	// with no repository.
	if msg, noRepo := req.settingsGateRefusal(t, off, repo); msg != "" {
		return workTreeUnknownPrefix + msg, ""
	} else if noRepo {
		return workTreeUnknownPrefix + "exit status 128", ""
	}
	u = req.use
	// This listing is light, and the rev-parse below runs its own heavy one. That is
	// the order of the listings of f6010b97 here (Linux VM, rows WR00 to WR15).
	c := hostileConfigRefusal(u, repo, false)
	// With git 2.56 a `.git` file to a gone target fails the listing with the gitfile
	// text. 5fd08069 then answers the error of its call about the repository (cells GF
	// and N04 of round tier0-27, Linux VM). With "shadowDirOff":true it answers the
	// listing refusal below (cell GFOFF of round task039-03, Linux VM).
	if c.gitFileText != "" && !bool(off) {
		return workTreeUnknownPrefix + c.gitFileText, ""
	}
	if c.refusal != "" {
		return workTreeUnknownPrefix + c.refusal, ""
	}
	if c.noRepo {
		return workTreeUnknownPrefix + noRepositoryHerePrefix + c.noRepoText, ""
	}
	// The call after that light listing is `rev-parse --show-toplevel` in baseRepo,
	// on f6010b97 (Linux VM, rows WR00, WR07, WR09 and WR14). Its answer does not
	// decide the reasons below. worktreeRootCheckoutRefusal compares the root with
	// it. If the D5 deadline stops the call, the reason is the exec error. That is
	// claustrum's choice (not measured).
	topLevel, killed := repoTopLevel(u, repo, &c.listing)
	if killed != nil {
		return workTreeUnknownPrefix + killed.Error(), ""
	}
	if baseIsGitDir(repo) {
		return workTreeUnknownPrefix + "exit status 128", ""
	}
	if err := repositoryCheckError(u, repo, nil); err != nil {
		return workTreeUnknownPrefix + err.Error(), ""
	}
	return "", topLevel
}

// noRepositoryHerePrefix starts the reason of git.worktree_remove with worktreeRoot when
// the listing says git finds no repository (externalWorkTreeRefusal).
const noRepositoryHerePrefix = "git finds no repository here: "

// unenterableBaseListing covers a baseRepo that exists but that git cannot start in:
// a regular file, or a directory without search permission (mode 0600). It then runs
// the configuration listing with baseRepo as the working directory and returns the
// refusal text with the start error. Go names the git that PATH resolves, for example
// "fork/exec /usr/bin/git: permission denied" or "fork/exec /usr/bin/git: not a
// directory". The frame matches f6010b97, measured side by side on a Linux VM (rows
// K10, K11 and K14). It returns "" for every other baseRepo that os.Stat reads, and
// runs nothing then.
//
// A third case is a baseRepo that os.Stat cannot follow and filepath.EvalSymlinks
// resolves. For a chain of 45 relative symlinks, 89cb6289 and f6010b97 answer the
// hooks refusal on a Linux VM. Its text ends with "chdir <baseRepo>: too many levels
// of symbolic links". They delete nothing (rows LNKb-g and LNKr-g: a bare repository
// and a work tree). Not measured: chain lengths other than 5 and 45, absolute link
// targets, a chain to a plain folder and a chain to nothing. Nor are a chain with a
// locked or marked worktree, and a path that is too long for the kernel.
//
// `git version` runs after the failed listing, as on 89cb6289 (rows L16a and L16b on
// a Linux VM). When it fails too, the text starts with gitCannotRunPrefix (see
// failedListingText). That case is not measured here. This `git version` gets the
// light environment, as on 89cb6289 (rows L16a and L16b on a Linux VM). In the
// symlink-chain rows LNKb-g and LNKr-g, the call log of 89cb6289 shows the read of
// the user excludes and no `git version` (Linux VM). claustrum does the same.
//
// The caller answers a PATH with no git before it calls this function.
func unenterableBaseListing(repo string) string {
	fi, err := os.Stat(repo)
	switch {
	case err != nil:
		// The kernel cannot follow baseRepo, but the daemon's own walk resolves it. A
		// chain of more symlinks than the kernel follows is one case.
		if _, werr := filepath.EvalSymlinks(repo); werr != nil {
			return ""
		}
	case fi.IsDir():
		if _, err := os.Lstat(filepath.Join(repo, ".git")); !errors.Is(err, fs.ErrPermission) {
			return ""
		}
	}
	ctx, cancel := gitCtx()
	defer cancel()
	// This listing does not use precursorEnv. Git never starts here, so its
	// environment never reaches git, and only the Go start error reaches the frame.
	r := runListing(ctx, repo, "", append(gitChildEnv(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:ssh"))
	var ee *exec.ExitError
	if r.err != nil && !errors.As(r.err, &ee) {
		if err != nil {
			// The symlink chain. The one git call of 89cb6289 is the read of the user
			// excludes, and no `git version` runs (rows LNKb-g and LNKr-g on a Linux VM,
			// LNKb-g on a macOS VM).
			userExcludesFile()
			return hooksRefusalPrefix + r.detail()
		}
		return failedListingText(gitUse{}, r, false)
	}
	return ""
}

// goneNonEntryGitFile reports whether the walk of the trust check found a `.git` file
// whose target is gone and is not shaped like a linked-worktree entry. The trust check
// answers "no repository" there. f6010b97 instead runs git, whose configuration
// listing fails, and answers the hooks refusal. A gone entry under .git/worktrees
// still answers "exit status 128". Measured side by side against f6010b97 on a Linux
// VM (rows K16 and WR13). A daemon GIT_DIR skips the walk, so it reports false then.
func goneNonEntryGitFile(repo string) bool {
	if os.Getenv("GIT_DIR") != "" {
		return false
	}
	start, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return false
	}
	if start, err = filepath.Abs(start); err != nil {
		return false
	}
	g, _, ok := findGitDir(start)
	if !ok {
		return false
	}
	_, err = os.Lstat(g)
	return errors.Is(err, fs.ErrNotExist) && !isWorktreeEntry(g)
}

// baseIsGitDir reports whether the walk of the trust check finds repo itself as the
// git directory, as for a bare repository or a .git directory. A daemon GIT_DIR skips
// the walk, so it reports false then. A directory inside a git directory is not
// measured, so it is not covered.
func baseIsGitDir(repo string) bool {
	if os.Getenv("GIT_DIR") != "" {
		return false
	}
	start, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return false
	}
	if start, err = filepath.Abs(start); err != nil {
		return false
	}
	g, _, ok := findGitDir(start)
	return ok && g == start
}

// externalWorktreeDirNotEmptyRefusal reports 7d193f89's refusal when the
// <directory> level (filepath.Dir(worktreePath)) already exists, carries no
// .claude-managed-worktrees marker, and is not empty — the per-repository directory
// under a worktree location must start out empty (or already be a managed one) so a
// create cannot silently graft a worktree into a directory of unrelated files.
// Returns "" when the directory is absent, empty, or already marked. The example
// filename is the first entry in os.ReadDir's sorted order — wire-visible, measured
// byte-for-byte against 7d193f89.
//
// The <directory> comes from the cleaned path. For "R/proj/w1/", filepath.Dir of
// the raw path is "R/proj/w1", not "R/proj". f6010b97 and 90fca6e6 refuse that
// create and name "R/proj" (measured on Linux and macOS VMs).
//
// The text names the <directory> through externalPathSpelling. With a worktreeRoot
// behind a symlink, 89cb6289 names it with the link resolved (cell U3a, Linux and
// macOS VMs).
func externalWorktreeDirNotEmptyRefusal(worktreePath string) string {
	dir := filepath.Dir(filepath.Clean(worktreePath))
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return ""
	}
	for _, e := range entries {
		if e.Name() == managedWorktreesMarker {
			return ""
		}
	}
	return fmt.Sprintf("refusing to create worktree: %s already exists, is not marked as a "+
		"worktree directory, and holds other files (for example %q); the per-repository "+
		"directory under a worktree location must start out empty — remove it, restore "+
		"its .claude-managed-worktrees file if you deleted it, or choose another location",
		externalPathSpelling(dir), entries[0].Name())
}

// managedWorktreesMarkerBody is the exact content 7d193f89 writes into a
// .claude-managed-worktrees marker on an external create (285 bytes,
// sha256 45da7f8a…). A client can read it via files.read, so the bytes are part of
// the observable contract and are reproduced verbatim — like the version spoof, this
// is measured reference OUTPUT, not copied source.
const managedWorktreesMarkerBody = "This directory holds Claude Code session worktrees placed here by the\n" +
	"claude-ssh daemon (the \"Worktree location\" setting). Its subdirectories are\n" +
	"managed checkouts; open the repository itself, not one of them, as a\n" +
	"session folder. Safe to delete once the directory is otherwise empty.\n"

// ensureManagedWorktreesMarker writes the marker into dir, the <directory> level of
// an external worktree with the symlinks of its root resolved. claustrum writes it
// after the parent step and before `git worktree add`. The directory is then
// tagged as managed before git runs.
//
// It creates the marker only when no entry of that name exists. An existing entry
// of any kind keeps its content and its mode, and a failed add keeps the marker.
// Any other open error stops the create with "cannot mark <dir> as a worktree
// location: openat .claude-managed-worktrees: <errno>". f6010b97 does the same on
// Linux and macOS VMs. Not measured: a write or close that fails after the open.
// The create goes on then.
func ensureManagedWorktreesMarker(dir string) error {
	f, err := os.OpenFile(filepath.Join(dir, managedWorktreesMarker), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return fmt.Errorf("cannot mark %s as a worktree location: openat %s: %w", dir, managedWorktreesMarker, err)
	}
	_, _ = f.WriteString(managedWorktreesMarkerBody)
	_ = f.Close()
	return nil
}
