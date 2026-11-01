package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The staged `worktree add` of git.worktree_create in the state shadow, as
// 5fd08069 runs it on a Windows VM and be1c1ca5 runs it on Linux, macOS and
// Windows. The call gets a folder of its own:
//
//  1. Before the call it makes `<S>\.s<n>` with a copy of the settings and of
//     each registration of the repository (addCopyEntries). The call carries
//     GIT_COMMON_DIR=<S>\.s<n>, so git writes the new registration there.
//  2. After a good call it removes old entries `.claude-new-worktree-*` of the
//     git directory (sweepOldStaging).
//  3. It copies the new registration into a new folder
//     `<git directory>\.claude-new-worktree-<16 hex>` and renames that folder to
//     `<git directory>\worktrees\<name>`.
//  4. It gives the new worktree a `.git` file that names that folder, and
//     removes `.s<n>`. be1c1ca5 adds at `<S>/.s<n>/leaf/<name>`, so git writes
//     no file into the new worktree itself.
//
// The rows are cells 44a to 44f of s3p2b-edges2-windows and cells 47 to 49 of
// s3p2b-edges3-windows (Windows VM, Git for Windows 2.55.0). On Linux 5fd08069
// makes no such folder or entry. The steps of be1c1ca5 are measured on Linux,
// macOS and Windows.
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
	// leafName is the last name of the new worktree, and of its folder in
	// `<dir>/leaf`.
	leafName string
}

// stagedLeafFolder is the folder of `.s<n>` that holds the path of the add.
const stagedLeafFolder = "leaf"

// privateCopyText starts the text of a create whose folder `.s<n>` cannot be
// made on Linux and macOS. be1c1ca5 answers it with `mkdir <S>/.s<n>:
// permission denied` after it, with errorCode worktree_add_failed and no
// other words, on Linux (a folder git-shadow of mode 555).
const privateCopyText = "cannot add a worktree: Claude could not prepare its own files on the remote machine: " +
	"cannot make Claude's private copy of this repository's git directory: "

// requestDirError is the error of a folder `.s<n>` that cannot be made.
type requestDirError struct{ err error }

func (e *requestDirError) Error() string { return e.err.Error() }
func (e *requestDirError) Unwrap() error { return e.err }

// addOwnFiles is the error of a `worktree add` that did not run, because a
// file of the daemon cannot be made. The create answers its text alone,
// with errorCode worktree_add_failed and the rollback of a failed add.
type addOwnFiles struct{ text string }

func (e *addOwnFiles) Error() string { return e.text }

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
		if err != nil {
			err = &requestDirError{err: err}
		}
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
	if err := writeCopyEntries(dir, "", addCopyEntries(head, config, exclude, hasExclude, regs), 0o700, 0o600); err != nil {
		return err
	}
	return dir.Mkdir(stagedLeafFolder, 0o700)
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
// whose steps after it did not run or failed. The create answers it with
// completingAddPrefix and rolls back. keepRegistration marks a moved
// registration that the rollback keeps (a `locked` file that cannot be
// removed).
type addIncomplete struct {
	text             string
	keepRegistration bool
}

func (e *addIncomplete) Error() string { return e.text }

// addRefused is the error of a `worktree add` call that git ended well, whose
// new worktree the daemon refuses before the staging folder is made, or after
// the move when the move removed the moved registration again. The create
// answers it with unsafe_path, and its rollback
// removes the leaf and the branch and no registration.
type addRefused struct{ text string }

func (e *addRefused) Error() string { return e.text }

// addNotPrepared is the error of a `worktree add` call that did not run,
// because a step before it failed (openAddStage). A create in attach mode then
// makes no second add with a new branch. With `existingBranch` and a file at
// `.git\worktrees`, 5fd08069 answers `git worktree add failed: cannot prepare
// the repository's worktrees directory: "C:\\o\\w\\repo\\.git\\worktrees" is
// not a directory`, one time, after 11 git calls (table S9 of
// val-s3p2b2-12-windows, Windows VM, 2 of 2 runs). claustrum answers the same
// after 10 calls (table S9 of val-s3p2b2-13-windows, 2 runs). The other failed
// steps are not measured in attach mode: they get the same rule. On Linux and
// macOS the refusal of worktreesFolderRefusal is such an error too. offLink
// marks the refusal of offWorktreesRefusal.
type addNotPrepared struct {
	text    string
	offLink bool
}

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
	return runAddStaged(ctx, u, hardenedGitCmd(ctx, u, repo, false, nil, args...), leaf, cp)
}

// runAddStaged runs the `worktree add` call cmd of the request u with the
// folder `.s<n>`, and the steps after a good call. On Linux and macOS
// worktreeAdd calls it after its own steps before the add.
//
// The call gets the path `<S>/.s<n>/leaf/<name>` in the place of leaf, where
// <name> is the last name of leaf. be1c1ca5 does so on Linux and Windows. Git
// then writes the `.git` file into that folder and not into leaf. The folder
// goes with `.s<n>`.
//
// A `.s<n>` that cannot be made answers privateCopyText on Linux and macOS, as
// be1c1ca5 does on Linux. Windows keeps the text of 5fd08069 (the maintainer's
// choice, not measured on be1c1ca5).
func runAddStaged(ctx context.Context, u gitUse, cmd *exec.Cmd, leaf string, cp worktreeCheckpoint) (string, error) {
	// The staging folder is filled from the copy, so it holds the folder of
	// the copy until it goes (from the code).
	drop, err := u.holdCopy(cmd.Args)
	if err != nil {
		return "", err
	}
	defer drop()
	st, err := openAddStage(u)
	if err != nil {
		var dirErr *requestDirError
		if !stagedAddWindowsSteps && errors.As(err, &dirErr) {
			return "", &addOwnFiles{text: privateCopyText + dirErr.err.Error()}
		}
		return "", &addNotPrepared{text: prepareWorktreesPrefix + err.Error()}
	}
	defer st.close()
	cmd.Env = withAddCopy(cmd.Env, u.repoDir, st.dir)
	st.leafName = filepath.Base(filepath.Clean(leaf))
	staged := filepath.Join(st.dir, stagedLeafFolder, st.leafName)
	for i, a := range cmd.Args {
		if i > 0 && a == leaf {
			cmd.Args[i] = staged
		}
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err = gitRun(ctx, cmd)
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
// be1c1ca5 answers the same text on Linux for a symbolic link at `.git` that
// was put there right after the add or right before it, and removes the leaf.
// After a link right after the add the branch stays. After a link right
// before it, be1c1ca5 deletes the branch through the link, and claustrum keeps
// it (runBranchStep, D24).
//
// On Linux and macOS a new real directory at the path that passes the gate
// takes the hold first (acceptedGitDir.replacedAtPath), and the move goes
// into it. be1c1ca5 moves the registration into a copy of `.git` made right
// after the add or right before it, and the create succeeds there (measured
// on Linux). The sweep is skipped then (claustrum's own). A rollback after
// that step deletes the moved registration by its identity through the new
// folder, as be1c1ca5 does after a failed checkout (measured on Linux, the
// maintainer's approval of 2026-11-01).
//
// Both steps act through the held folder, so a folder that is put at the path
// after this test gets nothing either (from the code: no row has a swap at
// that moment). A path that no longer names the held folder at the sweep, or
// a hold that moved before the add, gets no sweep (claustrum's own).
//
// A failed step of the move fails the create (addIncomplete or addRefused),
// with no log line (rounds task009-01 and task087-01, Windows VM). The steps
// that no row measured fail it too: the leaf has no `.git` file before the
// move ends (claustrum's own).
func (st *addStage) finish(leaf string, cp worktreeCheckpoint) error {
	replaced := false
	if st.u.swapped("staged") {
		var next *acceptedGitDir
		if !stagedAddWindowsSteps && st.u.accepted != nil {
			next = st.u.accepted.replacedAtPath(st.u.accepted.dir)
		}
		if next == nil {
			return &addIncomplete{text: realDirText(st.u.gitDir)}
		}
		st.u.accepted.adopt(next)
		st.git, replaced = st.u.accepted.root, true
		// The new folder gets `worktrees` when it has none, as the folder of
		// the gate did before the add (makeWorktreesFolder). be1c1ca5 makes the
		// registration in a second repository there (measured on Linux).
		if err := st.git.Mkdir(worktreesSubdir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return &addIncomplete{text: err.Error()}
		}
	}
	if !replaced && (st.u.accepted == nil || st.u.accepted.isAt(st.u.gitDir)) && !st.u.swapped("sweep") {
		sweepOldStaging(st.git, st.u.heldGitDir(), stagingClock())
	}
	if err := st.move(leaf, cp); err != nil {
		var incomplete *addIncomplete
		var refused *addRefused
		if errors.As(err, &incomplete) || errors.As(err, &refused) {
			return err
		}
		return &addIncomplete{text: err.Error()}
	}
	return nil
}

// stagingRename renames the staging folder of a create through root, the root
// at the git directory. A test replaces it. Production never does.
var stagingRename = func(root *os.Root, oldName, newName string) error {
	return root.Rename(oldName, newName)
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
// git is the root at that directory: the folder that the gate of the create
// accepted and holds open. addStage.finish calls it on every system, after
// its test that the path of the git directory still names the held folder.
// It skips the sweep after acceptedGitDir.replacedAtPath.
// now is the time after that call ended.
//
// THIS DELETES ENTRIES THAT THE DAEMON DID NOT MAKE. On 5fd08069 a create
// removes each direct entry of the git directory whose name starts with
// `.claude-new-worktree-` and whose own write time is 1 hour old or more: a
// folder with its file, an empty folder, a plain file and a junction (cell 48 of
// s3p2b-edges3-windows, 248 cells on a Windows VM). claustrum does the same on
// Windows by the maintainer's decision of 2026-10-11. On Linux and macOS the
// maintainer approved following be1c1ca5, with these guards, on 2026-10-28.
// gitDir comes from the `baseRepo` of
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
// state shadow. On Windows git.info and git.status leave every entry (120
// cells). On Linux and macOS be1c1ca5 removes old folders and links the same
// way (addStage.finish, measured on Linux and macOS). A create that fails, a
// request with the member and the state fallback-git are not measured, and
// they run no cleanup.
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
// directory, and gives the new worktree leaf its `.git` file.
//
// On 5fd08069 the daemon makes `<git directory>\.claude-new-worktree-<16 hex>`
// and fills it in place with new files: `HEAD` (19 bytes there), `commondir`
// (6), `gitdir` (38) and the folder `refs`. Then it renames the folder to
// `<git directory>\worktrees\<name>`: the folder and its files keep their file
// ids, in 9 of 9 runs (cell 47). After the move the `.git` file of the
// worktree has the text of worktreeGitFileText (cells 49a to 49f). The steps
// and their failures are those of round task087-01 (Windows VM), with the
// steps of be1c1ca5 (measured on Linux, macOS and Windows):
//
//  1. `.s<n>\worktrees` must hold one new name. Else the text is `git
//     registered <n> worktrees where one was expected; try again` (cell S1).
//  2. The `.git` file of the staged leaf must name that entry of `.s<n>`.
//     Else the create is refused with otherEntryRefusal (cell S2).
//  3. The leaf must hold no `.git` entry. Else the create is refused with
//     plantedGitFileText (be1c1ca5, measured on Linux). These tests come
//     before the staging folder is made.
//  4. The staging folder gets a copy of the whole tree of the registration
//     (copyRegistrationTree, cells S3 and S4), the modes of stagedEntryModes,
//     a `gitdir` record that names the leaf (stagedRecordText), and the file
//     `locked` with `initializing` and a newline (be1c1ca5, measured on Linux,
//     macOS and Windows).
//  5. The target is the first free name of registrationTarget (cells S5 and
//     S6v, and C4d of round task007-01).
//  6. The rename gets the retry of retryRename (cells W3a to W3c of round
//     task009-01). A rename that still fails ends the move.
//  7. The entry at the target must still be the staging folder. Else the text
//     is replacedEntryText, and nothing is removed (D24).
//  8. The leaf gets a new `.git` file that names the target. A `.git` there
//     refuses the create with plantedGitFileText. A failure there removes the
//     moved registration again (cell S8v).
//  9. `locked` goes by one plain remove (removeLockedFile).
//
// Each failure after the staging folder was made and before the rename
// removes that folder, by the name that this call made, through the held git
// directory. The step records the moved registration and its identity in the
// held git directory (acceptedGitDir.staged). A rollback removes that entry,
// and the index goes into it.
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
	if added := addedNames(st.copied, after); len(added) != 1 {
		return &addIncomplete{text: fmt.Sprintf("git registered %d worktrees where one was expected; try again", len(added))}
	}
	name, ok := newRegistrationName(st.copied, after)
	if !ok {
		return errors.New("git left no single new registration")
	}
	if !stagedLeafNamesEntry(dir, st.leafName, filepath.Base(st.dir), name, st.u.gitDir) {
		return &addRefused{text: otherEntryRefusal(leaf)}
	}
	if err := leafHoldsNoGitFile(leaf, cp); err != nil {
		return err
	}
	source := filepath.Join(worktreesSubdir, name)
	if fi, err := dir.Lstat(source); err != nil || !goesAsTree(fi.Mode()) {
		return errors.New("the new registration is no folder")
	}
	reg, err := dir.OpenRoot(source)
	if err != nil {
		return err
	}
	defer func() { _ = reg.Close() }()

	var salt [8]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return err
	}
	staging := stagingNamePrefix + hex.EncodeToString(salt[:])
	if err := st.git.Mkdir(staging, 0o755); err != nil {
		return err
	}
	// The move holds the staging folder open from its mkdir until it returns.
	// Its identity is that of the handle, taken before the rename, so the moved
	// registration keeps it. No other folder gets it while the handle is open
	// (claustrum's own). The handle is closed when move returns. On Windows an
	// open folder below `.git` blocks a rename of `.git` (from the code, and
	// round task087-03, Windows VM). The identity stays valid for the moved
	// folder after the close (from the code, not measured).
	var held *os.File
	var heldInfo os.FileInfo
	closeHeld := func() {
		if held != nil {
			_ = held.Close()
			held = nil
		}
	}
	defer closeHeld()
	fail := func(err error) error {
		closeHeld()
		st.removeOwnStaging(staging)
		return &addIncomplete{text: err.Error()}
	}
	held, err = st.git.Open(staging)
	if err == nil {
		heldInfo, err = held.Stat()
	}
	if err != nil {
		return fail(err)
	}
	if err := copyRegistrationTree(reg, st.git, staging, ""); err != nil {
		return fail(err)
	}
	if err := st.writeStagedRecord(reg, staging, leaf); err != nil {
		return fail(err)
	}
	if err := writeCopyEntries(st.git, staging, []copyEntry{{path: "locked", data: []byte(initializingLock)}}, 0o755, 0o644); err != nil {
		return fail(err)
	}
	stagedEntryModes(reg, st.git, staging)
	targetName, ok := registrationTarget(st.git, name)
	if !ok {
		return fail(fmt.Errorf("the repository already has a hundred worktrees called %q; remove some (git worktree prune, git worktree remove)", name))
	}
	target := filepath.Join(worktreesSubdir, targetName)
	if err := retryRename(func() error { return stagingRename(st.git, staging, target) }); err != nil {
		return fail(err)
	}
	// The staging folder stays open through the identity test, so no other
	// folder can take its identity before the test.
	entry, err := st.git.OpenRoot(target)
	if err == nil {
		fi, statErr := entry.Stat(".")
		if statErr != nil || !os.SameFile(fi, heldInfo) {
			_ = entry.Close()
			err = errors.New(replacedEntryText)
		}
	}
	closeHeld()
	if err != nil {
		return &addIncomplete{text: replacedEntryText}
	}
	defer func() { _ = entry.Close() }()
	if err := writeLeafGitFile(leaf, cp, worktreeGitFileText(st.u.heldGitDir(), targetName)); err != nil {
		st.removeMoved(target, heldInfo)
		if errors.Is(err, fs.ErrExist) {
			return &addRefused{text: plantedGitFileText(leaf)}
		}
		return &addIncomplete{text: err.Error()}
	}
	if err := removeLockedFile(entry); err != nil {
		return &addIncomplete{text: err.Error() + "; try again", keepRegistration: true}
	}
	if st.u.accepted != nil {
		st.u.accepted.staged = stagedEntry{name: targetName, info: heldInfo}
	}
	return nil
}

// initializingLock is the text of the file `locked` of a registration while
// the staging step completes it. be1c1ca5 writes `initializing` and a newline,
// 13 bytes (measured on Linux, macOS and Windows).
const initializingLock = "initializing\n"

// replacedEntryText is the cause of a create whose moved registration was
// replaced right after the rename. be1c1ca5 answers it after
// completingAddPrefix, with errorCode worktree_add_failed (measured on Linux).
// It then removes the replacing entry by its name. claustrum keeps that entry
// and the moved folder, because neither is the folder that it holds (D24).
const replacedEntryText = "the new worktree's entry was replaced while it was being moved into place; try again"

// plantedGitFileText is the refusal of a create whose leaf holds a `.git`
// entry that the daemon did not make. be1c1ca5 answers it with errorCode
// unsafe_path, and the leaf and the new branch go (measured on Linux). The
// path is in the form of tickPath.
func plantedGitFileText(leaf string) string {
	return "refusing to create worktree: " + tickPath(filepath.Join(leaf, ".git")) +
		" was put there by something else while the worktree was being made; nothing was written through it; try again"
}

// removeLockedFile removes the file `locked` of the moved registration by one
// plain remove through entry, the root at that registration. On Windows a
// held file gets the retry of retryWhile for renameRetryBound. be1c1ca5
// succeeds with the file held for 1 s and fails with it held for 3 s
// (measured on Windows). The error has the form `removeat locked: <cause>`, as
// be1c1ca5 answers it on Linux, macOS and Windows. A test replaces it.
var removeLockedFile = func(entry *os.Root) error {
	return retryWhile(renameRetryBound, renameHeld, func() error { return entry.Remove("locked") })
}

// writeStagedRecord gives the staging folder a `gitdir` record that names
// the `.git` file of leaf, when git wrote one that names the staged leaf.
// be1c1ca5 has `<leaf>/.git` and a newline there, with the links of the
// path resolved and a trailing blank kept (measured on Linux). Who writes
// it is not measured. A record of another form stays as git wrote it.
func (st *addStage) writeStagedRecord(reg *os.Root, staging, leaf string) error {
	old, ok := readRootFile(reg, "gitdir", registrationFileMaxBytes)
	if !ok {
		return nil
	}
	stagedGit := filepath.Join(st.dir, stagedLeafFolder, st.leafName, ".git")
	rec := strings.TrimSuffix(string(old), "\n")
	if filepath.Clean(filepath.FromSlash(rec)) != filepath.Clean(stagedGit) && !sameGitDirPath(resolveAsFarAsExists(filepath.FromSlash(rec)), resolveAsFarAsExists(stagedGit)) {
		return nil
	}
	path := filepath.Join(staging, "gitdir")
	fi, err := st.git.Lstat(path)
	if err != nil {
		return err
	}
	if err := st.git.Remove(path); err != nil {
		return err
	}
	if err := writeRootFile(st.git, path, []byte(stagedRecordText(leaf)), fi.Mode().Perm()); err != nil {
		return err
	}
	return st.git.Chmod(path, fi.Mode().Perm())
}

// stagedRecordText is the `gitdir` record of the new worktree leaf: the
// resolved path of leaf with forward slashes, `/.git` and a newline.
func stagedRecordText(leaf string) string {
	p := resolveAsFarAsExists(filepath.Clean(leaf))
	return filepath.ToSlash(p) + "/.git\n"
}

// stagedLeafNamesEntry reports whether the `.git` file of the staged leaf
// name in dir, the root at `.s<n>`, names the entry entry in the folder
// addName (stagedGitFileText).
func stagedLeafNamesEntry(dir *os.Root, name, addName, entry, gitDir string) bool {
	old, ok := readRootFile(dir, filepath.Join(stagedLeafFolder, name, ".git"), repointMaxBytes)
	if !ok {
		return false
	}
	_, ok = stagedGitFileText(string(old), addName, entry, gitDir)
	return ok
}

// leafHoldsNoGitFile tests that leaf is still the folder of the checkpoint cp
// and holds no `.git` entry. A `.git` there gives addRefused with
// plantedGitFileText.
func leafHoldsNoGitFile(leaf string, cp worktreeCheckpoint) error {
	if cp.info == nil {
		return errors.New("the create holds no checkpoint of the new worktree")
	}
	root, err := os.OpenRoot(leaf)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if fi, err := root.Stat("."); err != nil || !os.SameFile(fi, cp.info) {
		return errors.New("the new worktree is no longer the folder of the create")
	}
	if _, err := root.Lstat(".git"); err == nil {
		return &addRefused{text: plantedGitFileText(leaf)}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// addedNames are the names of after that are not in before.
func addedNames(before, after []string) []string {
	var added []string
	for _, name := range after {
		if !slices.Contains(before, name) {
			added = append(added, name)
		}
	}
	return added
}

// registrationTarget is the name that the moved registration takes in the
// folder `worktrees` of root, the git directory: the first of name, name1 to
// name99 that is not there. ok is false when all 100 are there. On 5fd08069 a
// folder w1 that was made after the add sends the move to w11 (cell S5). The
// 100 folders w1 and w11 to w199 fail the create (cell S6v, round task087-01,
// Windows VM).
func registrationTarget(root *os.Root, name string) (string, bool) {
	for i := range 100 {
		next := name
		if i > 0 {
			next += strconv.Itoa(i)
		}
		if _, err := root.Lstat(filepath.Join(worktreesSubdir, next)); errors.Is(err, fs.ErrNotExist) {
			return next, true
		}
	}
	return "", false
}

// copyRegistrationTree copies the entries of the folder rel of src, the root
// at the registration in `.s<n>`, into the folder base of dst, the held git
// directory, with new folders and new files. A real folder is copied with its
// content. Every other entry is read as a file through src. On 5fd08069 a file
// in a sub folder is copied (cell S3), and a junction fails the move with
// `openat j: path escapes from parent` (cell S4, round task087-01, Windows
// VM). The target of the junction stays. The bound of a file is claustrum's
// own.
func copyRegistrationTree(src, dst *os.Root, base, rel string) error {
	names, err := rootDirNames(src, cmp.Or(rel, "."))
	if err != nil {
		return err
	}
	for _, n := range names {
		path := filepath.Join(rel, n)
		fi, err := src.Lstat(path)
		if err != nil {
			return err
		}
		if goesAsTree(fi.Mode()) {
			if err := dst.Mkdir(filepath.Join(base, path), 0o755); err != nil {
				return err
			}
			if err := copyRegistrationTree(src, dst, base, path); err != nil {
				return err
			}
			continue
		}
		data, err := readRegistrationFile(src, path)
		if err != nil {
			return err
		}
		if err := writeCopyEntries(dst, base, []copyEntry{{path: filepath.ToSlash(path), data: data}}, 0o755, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// readRegistrationFile reads the file path of root, with at most
// registrationFileMaxBytes.
func readRegistrationFile(root *os.Root, path string) ([]byte, error) {
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, registrationFileMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > registrationFileMaxBytes {
		return nil, errors.New("the file " + path + " of the new registration is too large")
	}
	return data, nil
}

// writeLeafGitFile gives leaf a new `.git` file with text: one exclusive
// create with mode 0666, through a root at leaf, which must be the folder of
// the checkpoint cp. A `.git` entry there fails the create with fs.ErrExist,
// and nothing is written through it. The error of a failed step goes back.
func writeLeafGitFile(leaf string, cp worktreeCheckpoint, text string) error {
	if cp.info == nil {
		return errors.New("the create holds no checkpoint of the new worktree")
	}
	root, err := os.OpenRoot(leaf)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if fi, err := root.Stat("."); err != nil || !os.SameFile(fi, cp.info) {
		return errors.New("the new worktree is no longer the folder of the create")
	}
	return writeRootFile(root, ".git", []byte(text), 0o666)
}

// removeMoved removes the registration target that move renamed into place,
// after a failed write of the `.git` file of the leaf. It goes as a tree
// through the held root. On 5fd08069 the registration is gone 5 ms after the
// move (cell S8v of round task087-01, Windows VM). The remove runs only while
// the entry has the identity of the staging folder (moved), which move took
// before the rename. That test is claustrum's own.
func (st *addStage) removeMoved(target string, moved os.FileInfo) {
	now, err := st.git.Lstat(target)
	if err != nil || moved == nil || !os.SameFile(now, moved) || wipesHomeDir(filepath.Join(st.u.heldGitDir(), target)) {
		return
	}
	_ = removeHeldEntry(st.git, target)
}

// removeOwnStaging removes the staging folder name that move made in the git
// directory. It goes as a tree through the held root. wipesHomeDir runs first
// as defense in depth. A remove that fails logs one line (not measured).
func (st *addStage) removeOwnStaging(name string) {
	if wipesHomeDir(filepath.Join(st.u.heldGitDir(), name)) {
		logWarnf("[GitHandler] refusing to remove the staging folder %s of the git folder: it holds the home folder", name)
		return
	}
	if err := st.git.RemoveAll(name); err != nil {
		logWarnf("[GitHandler] the staging folder %s of the git folder was not removed: %v", name, err)
	}
}

// stagedRegistration is the registration that the staging step of this create
// moved into `worktrees` (acceptedGitDir.staged), with the identity of the
// handle that move held open, or the zero value. The index of the new worktree goes into it.
// A rollback removes it through the held git directory, by its name and its
// identity, and reads no gitdir record (removeTestedRegistration). On
// 5fd08069 a registration whose record was rewritten during the checkout goes
// at the rollback (cell S14a of round task087-01, Windows VM).
func (u gitUse) stagedRegistration() testedRegistration {
	if u.accepted == nil || u.accepted.staged.name == "" {
		return testedRegistration{}
	}
	return testedRegistration{
		path: filepath.Join(u.accepted.path, worktreesSubdir, u.accepted.staged.name),
		info: u.accepted.staged.info,
		git:  u.accepted,
	}
}

// stagedRecordRefusal is the refusal of a create whose moved registration has
// a gitdir record of another worktree, or "". The record is read through the
// held root. A relative record is resolved from the moved registration. On
// 5fd08069 a relative record, written by git in `.s<n>`, names another path
// after the move: the create answers adminRecordRefusal with unsafe_path, runs
// no checkout and removes nothing (cells S14 and S14n of round task087-01,
// Windows VM). An absolute record of the leaf passes (cell S14a). The
// resolution of links and short names before the compare is claustrum's own,
// and so is the refusal of a record that cannot be read.
func stagedRecordRefusal(a *acceptedGitDir, leaf string) string {
	rel := filepath.Join(worktreesSubdir, a.staged.name)
	record, err := readGitPlainFileIn(a.root, filepath.Join(rel, "gitdir"))
	if err == nil {
		rec := filepath.FromSlash(strings.TrimSpace(string(record)))
		if !filepath.IsAbs(rec) {
			rec = filepath.Join(a.path, rel, rec)
		}
		if sameGitDirPath(resolveAsFarAsExists(filepath.Dir(rec)), resolveAsFarAsExists(leaf)) {
			return ""
		}
	}
	return adminRecordRefusal(leaf)
}
