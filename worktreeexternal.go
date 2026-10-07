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
// Step 1 also refuses a worktreeRoot that is the file system root. For a root of "/"
// and a worktreePath of "/<name>", 89cb6289 sends the "is a filesystem root" text. The
// create sends it after 3 git calls, and the remove with no git call. That holds with
// the folder present or gone, and for a daemon that runs as root (Linux VM, cells R1n,
// R1u and R1r, and macOS VM, cells R1a and R1b). The macOS cells have the folder gone
// and a normal user. Nothing changes on disk. The calls place it before `rev-parse
// --show-toplevel`. Its place among the earlier tests is claustrum's choice. Not
// measured: its order against the ".." test, the worktreePath test, the baseRepo test
// and step 3. Not measured either: "//", "/." and each other spelling without ".."
// that cleans to the root.
// claustrum refuses them the same way and names the root as sent. A mount point that
// is not "/" passes this test (not measured).
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
	if isFilesystemRoot(worktreeRoot) {
		return fmt.Sprintf("refusing to %s worktree: %s is a filesystem root; choose the "+
			"worktree location by its absolute path, without %q, beneath the filesystem root",
			verb, worktreeRoot, "..")
	}
	return sessionFolderSpellingRefusal(worktreePath, verb)
}

// worktreeExternalShapeRefusal is step 3 of the checks listed above
// worktreeExternalSpellingRefusal. Callers run worktreeExternalSpellingRefusal first,
// so worktreePath is absolute and has no ".." component here.
func worktreeExternalShapeRefusal(worktreeRoot, worktreePath, verb string) string {
	root := filepath.Clean(worktreeRoot)
	wp := filepath.Clean(worktreePath)
	if filepath.Dir(filepath.Dir(wp)) != root {
		return fmt.Sprintf("refusing to %s worktree: %s is not "+
			"<worktree location>/<directory>/<name> beneath %s", verb, wp, root)
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

// worktreeList runs a light `git worktree list --porcelain -z` in repo, with its
// listing, and returns the entries in list order. git lists the main checkout first.
// It returns nil when the call fails. git.worktree_create with a worktreeRoot makes
// this call. git.worktree_remove uses worktreeListForRemove. The error is d5Kill of
// the call. Any other failure, such as a git older
// than 2.36 without `worktree list -z`, gives a nil error.
func worktreeList(repo string) ([]listedWorktree, error) {
	list, killed, err := worktreeListCall(repo, true)
	if err != nil && killed {
		return nil, err
	}
	return list, nil
}

// worktreeListCall runs a light `git worktree list --porcelain` in repo, with its
// listing. With nul it adds -z, and a NUL byte ends each line of the output. Without
// nul a LF ends each line. It returns the entries, whether the D5 deadline stopped the
// call, and its exec error.
func worktreeListCall(repo string, nul bool) (list []listedWorktree, killed bool, err error) {
	ctx, cancel := gitCtx()
	defer cancel()
	args, sep := []string{"worktree", "list", "--porcelain"}, "\n"
	if nul {
		args, sep = append(args, "-z"), "\x00"
	}
	b, err := hardenedGitCmd(ctx, repo, false, nil, args...).Output()
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
func worktreeListForRemove(repo string) (listed []listedWorktree, refusal string) {
	if _, err := os.Stat(repo); errors.Is(err, fs.ErrNotExist) {
		return nil, ""
	}
	listed, killed, err := worktreeListCall(repo, true)
	if err == nil {
		return listed, ""
	}
	if killed {
		return nil, workTreeUnknownPrefix + err.Error()
	}
	if listed, _, err = worktreeListCall(repo, false); err != nil {
		return nil, worktreeListRefusalPrefix + err.Error()
	}
	return listed, ""
}

// worktreeRootCheckoutRefusal is the refusal of a worktreeRoot that passes
// worktreeRootInRepoRefusal but leads into a checkout of the repository, or "" when
// the root passes. The main form comes first, then the linked form. So the main
// checkout wins over a linked worktree nearer to the root (rows Y6 and Y11c).
// topLevel is the answer of `rev-parse --show-toplevel` in baseRepo, and listed is
// the answer of worktreeList. verb is "create" or "remove".
func worktreeRootCheckoutRefusal(worktreeRoot, baseRepo, topLevel string, listed []listedWorktree, verb string) string {
	if msg := worktreeRootResolvesIntoRepoRefusal(worktreeRoot, baseRepo, topLevel, listed, verb); msg != "" {
		return msg
	}
	return worktreeRootInLinkedWorktreeRefusal(worktreeRoot, baseRepo, listed, verb)
}

// worktreeRootResolvesIntoRepoRefusal is the main form. It resolves the root as
// canonicalPathOfGone does: the symlinks of its longest existing prefix are resolved,
// and the missing rest is appended. Then it walks from that path up to "/".
// The outermost path on that walk that is the same file (device and inode) as
// topLevel, or as the main checkout, refuses the root. The text names that path, a
// prefix of the resolved root, and worktreeRoot and baseRepo as sent. It answers "" when neither
// checkout can be found. f6010b97 and 89cb6289 send the same frames on Linux and
// macOS VMs:
//   - A symlinked root, or one under a symlinked prefix that does not exist (Linux
//     rows Q6s, Q17, Q18, K4, K8 and E1, macOS rows MX12 and E1).
//   - A baseRepo below the top level, such as T/sub (Linux rows Q19, K9 and E2).
//   - A linked worktree W as baseRepo with the root in the main checkout T. The text
//     names T (rows W1, W3, Y6 and Y11c). The root in W itself names W (row Y4).
//   - A path that is the same file under another spelling: a bind mount (Linux row
//     Y8), a firmlink (macOS row Y8m) and a case variant (macOS row E8). macOS /tmp
//     gives "/private/tmp" (row E3). With baseRepo in a linked worktree, a bind
//     mount (Linux row T1) or a case variant (macOS row T1m) of the main checkout
//     matches too.
//   - When the root is under topLevel and under the main checkout, the main
//     checkout names the path (row T2b). It is also the outer one there, and
//     claustrum names the outer match.
func worktreeRootResolvesIntoRepoRefusal(worktreeRoot, baseRepo, topLevel string, listed []listedWorktree, verb string) string {
	var checkouts []os.FileInfo
	if topLevel != "" {
		if fi, err := os.Stat(topLevel); err == nil {
			checkouts = append(checkouts, fi)
		}
	}
	// A bare first entry has no main checkout, so it is not compared. That is
	// claustrum's choice (not measured).
	if len(listed) > 0 && !listed[0].bare {
		if fi, err := os.Stat(listed[0].path); err == nil {
			checkouts = append(checkouts, fi)
		}
	}
	if len(checkouts) == 0 {
		return ""
	}
	outer := ""
	for at := canonicalPathOfGone(worktreeRoot); ; at = filepath.Dir(at) {
		if fi, err := os.Stat(at); err == nil {
			for _, c := range checkouts {
				if os.SameFile(fi, c) {
					outer = at
				}
			}
		}
		if filepath.Dir(at) == at {
			break
		}
	}
	if outer == "" {
		return ""
	}
	return fmt.Sprintf("refusing to %s worktree: %s leads into the repository %s (at %s); "+
		"a worktree location must be outside the repository", verb, worktreeRoot, baseRepo, outer)
}

// worktreeRootInLinkedWorktreeRefusal is the linked form. It takes the entries of
// listed in list order, without the first one (the main checkout) and without an
// entry whose listed path does not exist. The first entry whose path, as listed, is
// the resolved root or a parent of it by whole components refuses the root. The test
// compares strings. It resolves no symlink in the listed path, folds no case and
// compares no file identity. The text names that entry twice, and worktreeRoot and
// baseRepo as sent. f6010b97 and 89cb6289 send the same frames on Linux and macOS
// VMs:
//   - Refused: the root is a linked worktree w0 of the repository, or lies in one
//     (rows W2, W4, Y1, Y2c, Y5 and Y11a). A root under a symlink into w0 that does
//     not exist (row Y10). Of two nested worktrees, the outer one is named (row Y6n).
//   - Passed: w0 listed through a symlinked spelling (row Y2), a case variant of w0
//     (macOS row Y3), a bind mount of w0 (Linux row Y8w), a root that holds w0 (row
//     Y7) and a w0 that was deleted (row Y9). A locked w0 that was deleted passes too
//     (row T3).
//
// Only a listed path that does not exist is skipped. Any other error of the lstat
// keeps the entry. That is claustrum's choice (not measured).
func worktreeRootInLinkedWorktreeRefusal(worktreeRoot, baseRepo string, listed []listedWorktree, verb string) string {
	resolved := canonicalPathOfGone(worktreeRoot)
	for i, w := range listed {
		if i == 0 {
			continue
		}
		if _, err := os.Lstat(w.path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if samePath(resolved, w.path) || pathStrictlyUnder(resolved, w.path) {
			return fmt.Sprintf("refusing to %s worktree: %s leads into %s, a worktree of the repository %s (at %s); "+
				"a worktree location must be outside the repository's checkouts", verb, worktreeRoot, w.path, baseRepo, w.path)
		}
	}
	return ""
}

// worktreeRootMissingRefusal is the refusal of git.worktree_remove for a
// worktreeRoot that does not exist or does not resolve, or "" when it resolves. It
// comes after worktreeRootCheckoutRefusal, before any later step of the removal, and
// nothing is deleted. The first slot is worktreeRoot as sent. f6010b97 and 89cb6289
// send the same frames on Linux and macOS VMs:
//   - A root that does not exist gives "<root> does not exist", with the root as
//     sent. That holds for a deleted linked worktree (rows Y9 and T3) and for two
//     missing levels (row T4).
//   - A symlink that does not resolve gives the error of filepath.EvalSymlinks, for
//     example "lstat <target>: no such file or directory" (row T5).
//
// claustrum sends other resolve errors the same way (not measured). Any other error
// of the lstat keeps the old answer. For "permission denied" that answer has the
// frame of the references (Linux row T6).
func worktreeRootMissingRefusal(worktreeRoot string) string {
	reason := worktreeRoot + " does not exist"
	_, err := os.Lstat(worktreeRoot)
	if err == nil {
		if _, err = filepath.EvalSymlinks(worktreeRoot); err == nil {
			return ""
		}
		reason = err.Error()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return fmt.Sprintf("failed to remove worktree: the worktree location %s is not reachable (%s); "+
		"nothing was removed — retry once it is available, or remove the worktree by hand", worktreeRoot, reason)
}

// repoTopLevel is the answer of a light `rev-parse --show-toplevel` in repo, or ""
// when the call fails. pre is nil when the call gets its own listing, else the
// listing it pins (see hardenedGitCmd). The error is d5Kill of the call.
func repoTopLevel(repo string, pre *configListing) (string, error) {
	ctx, cancel := gitCtx()
	defer cancel()
	b, err := hardenedGitCmd(ctx, repo, false, pre, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", d5Kill(ctx, err)
	}
	return strings.TrimRight(string(b), "\n"), nil
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
//     WR10 to WR13, K03 and K05). A `.git` file with a gone target that is not an
//     entry goes on to reason 4 (row K16).
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
// Its second answer is the git top level of baseRepo when the removal goes on, or ""
// when git cannot give it. worktreeRootCheckoutRefusal uses it.
func externalWorkTreeRefusal(repo string) (string, string) {
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
	switch t := requestGitDirTrust(repo, false); t.verdict {
	case gitDirRefused:
		return workTreeUnknownPrefix + t.refusal, ""
	case gitDirNoRepo:
		if !goneNonEntryGitFile(repo) {
			// The call log of 89cb6289 shows a light listing and `rev-parse
			// --show-toplevel` here, both with GIT_DIR=<null device>. The rev-parse
			// exits 128 (rows L13z-g and DG2h-g on a Linux VM). claustrum makes the two
			// calls and does not read their answers. Not measured: a listing that fails
			// there.
			if c := hostileConfigRefusal(repo, false); !c.refused() {
				_, _ = repoTopLevel(repo, &c.listing)
			}
			return workTreeUnknownPrefix + "exit status 128", ""
		}
	}
	// This listing is light, and the rev-parse below runs its own heavy one. That is
	// the order of the listings of f6010b97 here (Linux VM, rows WR00 to WR15).
	c := hostileConfigRefusal(repo, false)
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
	topLevel, killed := repoTopLevel(repo, &c.listing)
	if killed != nil {
		return workTreeUnknownPrefix + killed.Error(), ""
	}
	if baseIsGitDir(repo) {
		return workTreeUnknownPrefix + "exit status 128", ""
	}
	if err := repositoryCheckError(repo, nil); err != nil {
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
	r := runListing(ctx, repo, "", append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:ssh"))
	var ee *exec.ExitError
	if r.err != nil && !errors.As(r.err, &ee) {
		if err != nil {
			// The symlink chain. The one git call of 89cb6289 is the read of the user
			// excludes, and no `git version` runs (rows LNKb-g and LNKr-g on a Linux VM,
			// LNKb-g on a macOS VM).
			userExcludesFile()
			return hooksRefusalPrefix + r.detail()
		}
		return failedListingText(r, false)
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
		dir, entries[0].Name())
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
