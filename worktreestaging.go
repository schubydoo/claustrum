package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The `worktree add` of git.worktree_create on Windows in the state shadow, as
// 5fd08069 runs it on a Windows VM. The copy of a repository has no `worktrees`
// entry there (shadowLinksWorktrees), so that build gives the call a folder of
// its own:
//
//  1. Before the call it makes `<S>\.s<n>` with a copy of the settings and of
//     each registration of the repository (addCopyEntries). The call carries
//     GIT_COMMON_DIR=<S>\.s<n>, so git writes the new registration there.
//  2. After a good call it removes old entries `.claude-new-worktree-*` of the
//     git directory (sweepOldStaging).
//  3. It copies the new registration into a new folder
//     `<git directory>\.claude-new-worktree-<16 hex>` and renames that folder to
//     `<git directory>\worktrees\<name>`.
//  4. It gives the `.git` file of the new worktree the text that names that
//     folder, and removes `.s<n>`.
//
// The rows are cells 44a to 44f of s3p2b-edges2-windows and cells 47 to 49 of
// s3p2b-edges3-windows (Windows VM, Git for Windows 2.55.0). On Linux and macOS
// VMs no daemon makes such a folder or such an entry (s3p2-linux cell H1), and
// no step of this file runs there (shadowStagesAdd).
//
// The decisions are plain functions of their inputs, so that every system
// tests them. Every step in the git directory goes through one os.Root
// (addStage.git). In a create that is the folder that the gate of the request
// accepted and holds open (acceptedGitDir), so no step acts on another folder
// that was put at the path of the git directory.

const (
	// addCopyPrefix starts the name of the folder of one `worktree add`:
	// `.s<n>` with a decimal number that is new for each create (cell 44).
	addCopyPrefix = ".s"
	// stagingNamePrefix starts the name of a staging folder. 5fd08069 adds 16
	// hex digits (cells 44a and 47).
	stagingNamePrefix = ".claude-new-worktree-"
	// stagingMinAge is the age of the write time from which an old staging entry
	// goes. On 5fd08069 an entry of 3596.42 s stays and one of 3601.28 s goes
	// (cell 48). No cell has an age between the two.
	stagingMinAge = time.Hour
	// registrationFileMaxBytes bounds the read of one file of a registration.
	// The value is claustrum's own.
	registrationFileMaxBytes = 1 << 20
)

// registrationFiles are the files of a registration that get a copy in `.s<n>`
// when they exist. 5fd08069 copies `HEAD`, `commondir` and `gitdir`, and
// `locked` with its text where the registration has one. It copies no `index`,
// `ORIG_HEAD`, `logs` or `refs` (cells 49a to 49f).
var registrationFiles = []string{"HEAD", "commondir", "gitdir", "locked"}

// isStagingName reports whether name is the name of a staging entry: it starts
// with `.claude-new-worktree-`, with the dash. What follows the dash is not
// tested. On 5fd08069 `.claude-new-worktree-` alone goes, and so do the names
// with `old1`, with 16 hex digits and with 16 hex digits in upper case.
// `.claude-new-worktree`, `.claude-new-worktreeX` and
// `.claude-old-worktree-0123456789abcdef` stay (cell 48).
func isStagingName(name string) bool {
	return strings.HasPrefix(name, stagingNamePrefix)
}

// stagingEntryIsOld reports whether an entry whose own write time is written
// goes at the time now: its age is stagingMinAge or more.
func stagingEntryIsOld(written, now time.Time) bool {
	return now.Sub(written) >= stagingMinAge
}

// goesAsTree reports whether an old staging entry with the mode of its own
// Lstat is removed with its content. Only a real folder is. A plain file, a
// symbolic link and a junction go as that one entry: Go reports a junction as
// an irregular file. On 5fd08069 a junction goes and its target stays, with the
// file in it (cell 48, 30 of 30 junction cells).
func goesAsTree(mode fs.FileMode) bool {
	return mode.IsDir() && mode&(fs.ModeSymlink|fs.ModeIrregular) == 0
}

// copyEntry is one entry of the folder `.s<n>`: a folder, or a file with its
// content. path has forward slashes.
type copyEntry struct {
	path string
	dir  bool
	data []byte
}

// registrationCopy is one registration of the repository as it goes into
// `.s<n>`: its name and the files of registrationFiles that it has.
type registrationCopy struct {
	name  string
	files []copyEntry
}

// addCopyEntries lists the entries of `.s<n>` before git runs, in the order in
// which the daemon makes them. head and config are the HEAD and the config of
// the copy of the repository. exclude is its `info/exclude`, when hasExclude.
//
// The six states of cell 49 give these entries on 5fd08069:
//
//   - `HEAD` (21 bytes there), `config` (the text of `<R>\config`, 212 bytes),
//     `info\exclude` (240 bytes) and the empty folders `objects` and `refs`, in
//     every state.
//   - For each registration a folder `worktrees\<name>` with `HEAD`, `commondir`
//     and `gitdir`, and with `locked` where the registration has that file (0
//     bytes in cell 49d, 18 bytes in cell 49e). A registration whose worktree
//     folder is gone is copied as it is (cell 49f).
//   - No `worktrees` folder for a repository with no registration (cell 49b).
//
// A repository with no `info/exclude` is not measured: `info` is then empty.
// The other files of the copy (`shallow`, `info/attributes`, the files of a
// rebase) are not measured either, and they get no entry.
func addCopyEntries(head, config, exclude []byte, hasExclude bool, regs []registrationCopy) []copyEntry {
	entries := []copyEntry{
		{path: "HEAD", data: head},
		{path: "config", data: config},
		{path: "info", dir: true},
	}
	if hasExclude {
		entries = append(entries, copyEntry{path: "info/exclude", data: exclude})
	}
	entries = append(entries, copyEntry{path: "objects", dir: true}, copyEntry{path: "refs", dir: true})
	if len(regs) == 0 {
		return entries
	}
	entries = append(entries, copyEntry{path: worktreesSubdir, dir: true})
	for _, reg := range regs {
		base := worktreesSubdir + "/" + reg.name
		entries = append(entries, copyEntry{path: base, dir: true})
		for _, f := range reg.files {
			entries = append(entries, copyEntry{path: base + "/" + f.path, data: f.data})
		}
	}
	return entries
}

// worktreeGitFileText is the text of the `.git` file of a new worktree after
// the staging step: `gitdir: `, the git directory with forward slashes,
// `/worktrees/<name>` and a newline. On 5fd08069 the file is
// `gitdir: C:/o/w/repo/.git/worktrees/w1` and a newline, 38 bytes, for the git
// directory `C:\o\w\repo\.git` (cells 44a and 49a to 49f).
func worktreeGitFileText(gitDir, name string) string {
	return "gitdir: " + strings.ReplaceAll(gitDir, `\`, "/") + "/" + worktreesSubdir + "/" + name + "\n"
}

// stagedGitFileText is the new text of the `.git` file of a new worktree whose
// text is old, or false when old does not name the registration name in the
// folder addName. git writes that file with the path of the registration in
// `.s<n>`: it has 94 bytes on 5fd08069, the length of `gitdir: <S>/.s<n>/worktrees/w1`
// with forward slashes and a newline (cell 44a). Its text was not read there,
// so only its start and its end are tested here.
func stagedGitFileText(old, addName, name, gitDir string) (string, bool) {
	norm := strings.ReplaceAll(old, `\`, "/")
	if !strings.HasPrefix(norm, "gitdir: ") || !strings.HasSuffix(norm, "/"+addName+"/"+worktreesSubdir+"/"+name+"\n") {
		return "", false
	}
	return worktreeGitFileText(gitDir, name), true
}

// newRegistrationName is the one name of after that is not in before, or false
// when there is none or more than one. before has the registrations that the
// daemon copied into `.s<n>`, and after is the listing of `.s<n>\worktrees`
// after the call. git adds one folder there in all six states of cell 49.
func newRegistrationName(before, after []string) (string, bool) {
	var added []string
	for _, name := range after {
		if !slices.Contains(before, name) {
			added = append(added, name)
		}
	}
	if len(added) != 1 || added[0] == "." || added[0] == ".." || strings.ContainsAny(added[0], `/\`) {
		return "", false
	}
	return added[0], true
}

// withAddCopy is the environment env of a `worktree add` call with
// GIT_COMMON_DIR=<addDir> in the place of GIT_COMMON_DIR=<repoDir>. The call
// keeps its GIT_DIR, GIT_OBJECT_DIRECTORY and GIT_WORK_TREE (cells 44a and 49b).
func withAddCopy(env []string, repoDir, addDir string) []string {
	out := slices.Clone(env)
	for i, kv := range out {
		if kv == "GIT_COMMON_DIR="+repoDir {
			out[i] = "GIT_COMMON_DIR=" + addDir
		}
	}
	return out
}

// addStage is the state of one staged `worktree add`.
type addStage struct {
	u gitUse
	// git is the root at the git directory of the repository. In a create it
	// is the folder that the request holds (gitUse.accepted). With no held
	// folder it is opened before the call, and own is true: close then closes
	// it.
	git *os.Root
	own bool
	// dir is the folder `.s<n>`, and removeDir removes it.
	dir       string
	removeDir func()
	// copied has the names of the registrations that went into dir.
	copied []string
}

// openAddStage makes the folder `.s<n>` for one `worktree add` of the request
// u. The steps and their order are those of cell 49b: the listing of the call
// ended at 588.0 ms, `<git directory>\worktrees` was there at 589.8 ms, `.s<n>`
// at 592.9 ms, and the call started at 612.4 ms.
//
// The folder `worktrees` of the git directory is made when there is none, with
// the fixed name, through the root. A step that fails stops the create before
// the call. The texts of 5fd08069 are on a Windows VM (val-s3p2b2-7-windows):
//
//   - The git directory is no longer the folder that the gate accepted: the
//     refusal of its config, `this repository's git config cannot be used:
//     "<git directory>\\config": is, or is reached through, a link …`. 5fd08069
//     answers it for a junction at `.git` to a copy of the git directory and to
//     a folder that is no git directory (cells X1c-09 and X1p-09). It makes
//     nothing in the target of the junction. Another real directory at that
//     path is not measured: it gets the same text. With a file `gitdir: <X>`
//     at that path the text is `".git" is a link or a file where Claude needs
//     a real directory; …` (part C and cells CX of val-s3p2b2-12-windows, the
//     file at point 6 and at the listing of the add, 2 of 2 runs each).
//     claustrum answers that text in both rows (table C of
//     val-s3p2b2-13-windows, Windows VM, 2 runs each).
//   - `worktrees` is a file: `"<git directory>\\worktrees" is not a directory`
//     (table W3, with 8 git calls on both builds). A link or a junction there
//     is not measured: it gets the same text.
//   - `.s<n>` cannot be made: `mkdir <S>\.s<n>: Access is denied.` (tables W3
//     and F1, with a deny entry on the folder of the daemon and the two
//     privileges of section 1.6 off). The error comes from gitUse.requestDir.
//
// Every other failed step is not measured: it has the error of that step.
func openAddStage(u gitUse) (*addStage, error) {
	st := &addStage{u: u}
	switch {
	case u.accepted != nil:
		if u.swapped("prepare") {
			if fi, err := os.Lstat(u.gitDir); err == nil && fi.Mode().IsRegular() {
				return nil, errors.New(realDirText(u.gitDir))
			}
			return nil, errors.New(repoConfigPrefix + gitDirLinkText(u.gitDir))
		}
		if createStepHook != nil {
			createStepHook("prepared")
		}
		st.git = u.accepted.root
	case gitDirMustBeReal:
		return nil, errors.New("the create holds no git directory")
	default:
		git, err := os.OpenRoot(u.gitDir)
		if err != nil {
			return nil, err
		}
		st.git, st.own = git, true
	}
	err := st.git.Mkdir(worktreesSubdir, 0o755)
	if errors.Is(err, fs.ErrExist) {
		err = nil
		if fi, statErr := st.git.Lstat(worktreesSubdir); statErr == nil && !goesAsTree(fi.Mode()) {
			err = errors.New(quoteOperand(filepath.Join(u.gitDir, worktreesSubdir)) + " is not a directory")
		}
	}
	var regs []registrationCopy
	if err == nil {
		regs, err = readRegistrations(st.git)
	}
	if err == nil {
		st.dir, st.removeDir, err = u.requestDir(addCopyPrefix, "")
	}
	if err == nil {
		err = st.fill(regs)
	}
	if err != nil {
		st.close()
		return nil, err
	}
	return st, nil
}

// close removes `.s<n>` and closes a root that the stage opened. 5fd08069
// removes the folder after a good call and after a failed one (cells 44a to
// 44e). The removal goes through shadowHost.removeEntry (gitUse.requestDir).
func (st *addStage) close() {
	if st.removeDir != nil {
		st.removeDir()
	}
	if st.own {
		_ = st.git.Close()
	}
}

// readRegistrations reads the registrations of the repository through git, the
// root at its git directory. A registration is a real folder in `worktrees`. A
// link or a junction there is no registration: nothing follows it. Each file of
// registrationFiles is read when it is a regular file of at most
// registrationFileMaxBytes. A registration with a file that is missing is not
// measured: it gets the files that it has.
func readRegistrations(git *os.Root) ([]registrationCopy, error) {
	names, err := rootDirNames(git, worktreesSubdir)
	if err != nil {
		return nil, err
	}
	var regs []registrationCopy
	for _, name := range names {
		base := filepath.Join(worktreesSubdir, name)
		if fi, err := git.Lstat(base); err != nil || !goesAsTree(fi.Mode()) {
			continue
		}
		reg := registrationCopy{name: name}
		for _, file := range registrationFiles {
			if data, ok := readRootFile(git, filepath.Join(base, file), registrationFileMaxBytes); ok {
				reg.files = append(reg.files, copyEntry{path: file, data: data})
			}
		}
		regs = append(regs, reg)
	}
	return regs, nil
}

// rootDirNames lists the folder dir through root and returns its names in
// sorted order.
func rootDirNames(root *os.Root, dir string) ([]string, error) {
	d, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(-1)
	slices.Sort(names)
	return names, err
}

// readRootFile reads the regular file path through root. ok is false for an
// entry that is no regular file, that has more than max bytes, or that cannot
// be read.
func readRootFile(root *os.Root, path string, max int64) (data []byte, ok bool) {
	if fi, err := root.Lstat(path); err != nil || !fi.Mode().IsRegular() || fi.Size() > max {
		return nil, false
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	data, err = io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(data)) > max {
		return nil, false
	}
	return data, true
}

// fill writes the entries of `.s<n>` (addCopyEntries). HEAD, config and
// `info/exclude` come from the copy of the repository, which this request
// brought up to date. Each file is written in place: a poll of 5fd08069 saw a
// file with 0 bytes first and with its full size 4 to 5 ms later (cell 49).
func (st *addStage) fill(regs []registrationCopy) error {
	repo, err := os.OpenRoot(st.u.repoDir)
	if err != nil {
		return err
	}
	defer func() { _ = repo.Close() }()
	head, err := repo.ReadFile("HEAD")
	if err != nil {
		return err
	}
	config, err := repo.ReadFile("config")
	if err != nil {
		return err
	}
	exclude, err := repo.ReadFile(filepath.Join("info", "exclude"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	hasExclude := err == nil
	dir, err := os.OpenRoot(st.dir)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	for _, reg := range regs {
		st.copied = append(st.copied, reg.name)
	}
	return writeCopyEntries(dir, "", addCopyEntries(head, config, exclude, hasExclude, regs), 0o700, 0o600)
}

// writeCopyEntries makes entries below the folder base of root, as new folders
// and new files.
func writeCopyEntries(root *os.Root, base string, entries []copyEntry, dirMode, fileMode fs.FileMode) error {
	for _, e := range entries {
		path := filepath.Join(base, filepath.FromSlash(e.path))
		if e.dir {
			if err := root.Mkdir(path, dirMode); err != nil {
				return err
			}
			continue
		}
		f, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
		if err != nil {
			return err
		}
		_, err = f.Write(e.data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// addIncomplete is the error of a `worktree add` call that git ended well and
// whose steps after it did not run: the git directory is no longer the folder
// that the create holds. The create answers it with completingAddPrefix and
// rolls back.
type addIncomplete struct{ text string }

func (e *addIncomplete) Error() string { return e.text }

// addNotPrepared is the error of a `worktree add` call that did not run,
// because a step before it failed (openAddStage). A create in attach mode then
// makes no second add with a new branch. With `existingBranch` and a file at
// `.git\worktrees`, 5fd08069 answers `git worktree add failed: cannot prepare
// the repository's worktrees directory: "C:\\o\\w\\repo\\.git\\worktrees" is
// not a directory`, one time, after 11 git calls (table S9 of
// val-s3p2b2-12-windows, Windows VM, 2 of 2 runs). claustrum answers the same
// after 10 calls (table S9 of val-s3p2b2-13-windows, 2 runs). The other failed
// steps are not measured in attach mode: they get the same rule. On Linux and
// macOS the refusal of worktreesFolderRefusal is such an error too.
type addNotPrepared struct{ text string }

func (e *addNotPrepared) Error() string { return e.text }

// worktreeAddStaged runs one `worktree add` of git.worktree_create with the
// folder `.s<n>`, and the steps after a good call. The listing before the call
// carries GIT_COMMON_DIR=<copy of the repository>, and the call carries
// GIT_COMMON_DIR=<S>\.s<n> (cell 49b, the call log of 5fd08069).
//
// A failed step before the call gives its error with prepareWorktreesPrefix,
// and git does not run (openAddStage). The listing comes before those steps:
// 5fd08069 makes it and then stops (table W3).
func worktreeAddStaged(ctx context.Context, u gitUse, repo, leaf string, cp worktreeCheckpoint, args ...string) (string, error) {
	cmd := hardenedGitCmd(ctx, u, repo, false, nil, args...)
	st, err := openAddStage(u)
	if err != nil {
		return "", &addNotPrepared{text: prepareWorktreesPrefix + err.Error()}
	}
	defer st.close()
	cmd.Env = withAddCopy(cmd.Env, u.repoDir, st.dir)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err = cmd.Run()
	if err == nil {
		err = st.finish(leaf, cp)
	}
	return errBuf.String(), err
}

// finish runs the steps after a good call: the cleanup of old staging entries,
// then the move of the new registration. On 5fd08069 the call ended at 1252 ms,
// an old entry was seen for the last time at 1257 ms, and the daemon's own
// staging folder was seen at 1260 ms (cell 48, control M1).
//
// Neither step runs when the git directory is no longer the folder that the
// create holds. With a junction at `.git` after the call, 5fd08069 answers
// `git worktree add failed (completing the new worktree's git files): ".git"
// is a link or a file where Claude needs a real directory; …` and rolls back.
// An old entry in the target of the junction stays, and the target gets no
// registration (cells X1c-10 and X1p-10 of val-s3p2b2-7-windows, Windows VM).
// Both steps act through the held folder, so a folder that is put at the path
// after this test gets nothing either (from the code: no row has a swap at
// that moment).
//
// A move that fails is not measured. The daemon logs one line and goes on. The
// `.git` file of the new worktree then names no registration of the repository.
// No test after the add refuses the create on Windows. The end state is not
// measured.
func (st *addStage) finish(leaf string, cp worktreeCheckpoint) error {
	if st.u.swapped("added") {
		return &addIncomplete{text: realDirText(st.u.gitDir)}
	}
	sweepOldStaging(st.git, st.u.gitDir, stagingClock())
	if err := st.move(leaf, cp); err != nil {
		logWarnf("[GitHandler] the registration of the new worktree did not move into the git folder: %v", err)
	}
	return nil
}

// stagingClock is the clock of the cleanup. A test replaces it.
var stagingClock = time.Now

// removeStagingEntry removes one old staging entry through root, the root at
// the git directory. A real folder goes with its content, and every other kind
// goes as that one entry. A test replaces it. Production never does.
var removeStagingEntry = func(root *os.Root, name string, tree bool) error {
	if tree {
		return root.RemoveAll(name)
	}
	return root.Remove(name)
}

// sweepOldStaging removes the old staging entries of the git directory gitDir.
// git is the root at that directory. In a create it is the folder that the gate
// of the request accepted and holds open, and finish tests before this step
// that the path of the git directory still names it.
// now is the time after that call ended.
//
// THIS DELETES ENTRIES THAT THE DAEMON DID NOT MAKE. On 5fd08069 a create
// removes each direct entry of the git directory whose name starts with
// `.claude-new-worktree-` and whose own write time is 1 hour old or more: a
// folder with its file, an empty folder, a plain file and a junction (cell 48 of
// s3p2b-edges3-windows, 248 cells on a Windows VM). claustrum does the same by
// the maintainer's decision of 2026-10-11. gitDir comes from the `baseRepo` of
// the request through the trust check, so every guard is here, in this order:
//
//  1. The entries are listed through the root, and every step below acts
//     through the root. No path is built from a value of the request, and no
//     path is resolved again: a junction that is put at the path of the git
//     directory while the create runs does not lead the cleanup to its target.
//     5fd08069 leaves the target as it is (cells X1c of val-s3p2b2-7-windows,
//     Windows VM), and so does claustrum (72 swap runs, section 1.3 of
//     val-s3p2b2-12-windows).
//  2. The name must start with the fixed prefix (isStagingName). The names come
//     from the listing, so each is one name of the folder itself.
//  3. The entry itself is read with Lstat through the root. Nothing follows a
//     link or a junction.
//  4. Only the write time of that entry counts (stagingEntryIsOld). Its access
//     time, its creation time and the times of what is inside do not (cell 48).
//  5. wipesHomeDir refuses an entry that is the home folder or holds it.
//  6. Only a real folder goes with its content (goesAsTree), through
//     Root.RemoveAll of the one name. A file, a link and a junction go through
//     Root.Remove, so the target of a link stays.
//
// An entry that cannot be removed is not measured: the daemon logs one line
// and goes on, and the create is not refused for it. On 5fd08069 every create
// succeeded, also where an entry went.
//
// Only git.worktree_create comes here, after a good `worktree add` in the
// state shadow on Windows. git.info and git.status leave every entry (120
// cells). A create that fails, a request with the member and the state
// fallback-git are not measured, and they run no cleanup.
func sweepOldStaging(git *os.Root, gitDir string, now time.Time) {
	names, err := rootDirNames(git, ".")
	if err != nil {
		return
	}
	for _, name := range names {
		if !isStagingName(name) {
			continue
		}
		fi, err := git.Lstat(name)
		if err != nil || !stagingEntryIsOld(fi.ModTime(), now) {
			continue
		}
		if wipesHomeDir(filepath.Join(gitDir, name)) {
			logWarnf("[GitHandler] refusing to remove the old entry %s of the git folder: it holds the home folder", name)
			continue
		}
		if err := removeStagingEntry(git, name, goesAsTree(fi.Mode())); err != nil {
			logWarnf("[GitHandler] the old entry %s of the git folder was not removed: %v", name, err)
		}
	}
}

// move brings the registration that git wrote into `.s<n>` to the git
// directory, and points the `.git` file of the new worktree leaf at it.
//
// On 5fd08069 the daemon makes `<git directory>\.claude-new-worktree-<16 hex>`
// and fills it in place with new files: `HEAD` (19 bytes there), `commondir`
// (6), `gitdir` (38) and the folder `refs`. Then it renames the folder to
// `<git directory>\worktrees\<name>`: the folder and its files keep their file
// ids, in 9 of 9 runs (cell 47). No delete inside the git directory was seen
// for this step. After the move the `.git` file of the worktree has the text of
// worktreeGitFileText (cells 49a to 49f). The order of the rename and of that
// write is not measured: a poll saw both in one round.
//
// The copy takes the direct entries of the registration: each regular file, and
// each real folder as an empty folder. A folder with content is not measured.
// The registration is the one folder of `.s<n>\worktrees` that the daemon did
// not put there (newRegistrationName). A folder at the place of the
// registration in the git directory is not replaced. A staging folder that is
// left after an error is not removed here: a later create removes it when it
// is old (sweepOldStaging).
func (st *addStage) move(leaf string, cp worktreeCheckpoint) error {
	dir, err := os.OpenRoot(st.dir)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	after, err := rootDirNames(dir, worktreesSubdir)
	if err != nil {
		return err
	}
	name, ok := newRegistrationName(st.copied, after)
	if !ok {
		return errors.New("git left no single new registration")
	}
	source := filepath.Join(worktreesSubdir, name)
	if fi, err := dir.Lstat(source); err != nil || !goesAsTree(fi.Mode()) {
		return errors.New("the new registration is no folder")
	}
	names, err := rootDirNames(dir, source)
	if err != nil {
		return err
	}
	var entries []copyEntry
	for _, n := range names {
		path := filepath.Join(source, n)
		fi, err := dir.Lstat(path)
		switch {
		case err != nil:
			return err
		case goesAsTree(fi.Mode()):
			entries = append(entries, copyEntry{path: n, dir: true})
		case fi.Mode().IsRegular():
			data, ok := readRootFile(dir, path, registrationFileMaxBytes)
			if !ok {
				return errors.New("the file " + n + " of the new registration cannot be read")
			}
			entries = append(entries, copyEntry{path: n, data: data})
		}
	}

	var salt [8]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return err
	}
	staging := stagingNamePrefix + hex.EncodeToString(salt[:])
	if err := st.git.Mkdir(staging, 0o755); err != nil {
		return err
	}
	if err := writeCopyEntries(st.git, staging, entries, 0o755, 0o644); err != nil {
		return err
	}
	target := filepath.Join(worktreesSubdir, name)
	if _, err := st.git.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		return errors.New("the git folder already has an entry " + target)
	}
	if err := st.git.Rename(staging, target); err != nil {
		return err
	}
	replaced := replaceWorktreeGitFile(leaf, cp, func(old string) (string, bool) {
		return stagedGitFileText(old, filepath.Base(st.dir), name, st.u.gitDir)
	})
	if !replaced {
		return errors.New("the .git file of the new worktree was not written")
	}
	return nil
}
