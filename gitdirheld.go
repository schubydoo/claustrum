package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The held git directory of a git.worktree_create (acceptedGitDir), and the
// steps that act through it. This comment is the home of the step list.
//
// A create that holds its git directory makes each delete, rename, mkdir and
// file write inside that directory through the held folder
// (acceptedGitDir.root), not through the path. A link, a junction or another
// folder that is put at the path while the create runs gets none of them.
//
//   - The stale registration before the add: dropStaleRegistration on Windows,
//     and dropStaleWorktreeRegistration with registrationsRoot on Linux and
//     macOS.
//   - The folder `worktrees`: makeWorktreesFolder, and openAddStage on Windows.
//   - The Windows staging steps: openAddStage, sweepOldStaging, addStage.move.
//   - The `config.worktree` file of the registration: writeWorktreeConfig.
//   - The index: installIndex on Windows, and installTestedWorktreeIndex with
//     registrationsRoot on Linux and macOS.
//   - The registration of a rollback: removeCreatedRegistration, and
//     removeTestedRegistration with registrationsRoot on Linux and macOS.
//
// By the path stay:
//
//   - Git itself, which takes the git directory as a path.
//   - A request that holds no git directory: "shadowDirOff":true, the state
//     fallback-git and a server with no run folder.
//   - On Linux and macOS, a `worktrees` entry that is a symbolic link, and the
//     registrations folder of another git directory (registrationsRoot).
//   - The reads of the create that decide a refusal.
//
// What 5fd08069 does with a swapped git directory is in the section "A git
// directory that is replaced while a create runs" of
// docs/record/git-settings-copy.md. No row has a swap between the test of a
// step and one of these acts.

// removeHeldEntry removes the entry name with its content through root, the
// held git directory. A test replaces it. Production never does.
var removeHeldEntry = func(root *os.Root, name string) error {
	return root.RemoveAll(name)
}

// registrationName is the name of the registration at path, when path is a
// direct entry of the folder `worktrees` of the held git directory. The paths
// are compared as texts (sameGitDirPath). A registration that the path spells
// in another way is not found, and the steps below then leave it (not
// measured).
func (a *acceptedGitDir) registrationName(path string) (string, bool) {
	reg := filepath.Clean(path)
	name := filepath.Base(reg)
	if path == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", false
	}
	if !sameGitDirPath(filepath.Dir(reg), filepath.Join(filepath.Clean(a.path), worktreesSubdir)) {
		return "", false
	}
	return name, true
}

// removeRegistration removes the registration name of the held git directory
// as a tree: one RemoveAll of `worktrees/<name>` through the held root.
// wipesHomeDir refuses an entry that is the home folder or holds it.
func (a *acceptedGitDir) removeRegistration(name string) {
	if wipesHomeDir(filepath.Join(a.path, worktreesSubdir, name)) {
		return
	}
	_ = removeHeldEntry(a.root, filepath.Join(worktreesSubdir, name))
}

// dropStaleRegistration is the Windows step of a create before the leaf is
// made, for a create that holds its git directory. It removes the registration whose
// `gitdir` record names worktreePath, which the caller found missing. The rule
// is that of the Windows step (dropStaleWorktreeRegistration): the first match
// in name order goes, with no test of a `locked` file. The listing, the read of
// each record and the delete go through the held root. A registration is a
// real folder: a link or a junction in `worktrees` is passed over.
func (a *acceptedGitDir) dropStaleRegistration(worktreePath string) {
	names, err := rootDirNames(a.root, worktreesSubdir)
	if err != nil {
		return
	}
	target := resolveAsFarAsExists(worktreePath)
	for _, name := range names {
		rel := filepath.Join(worktreesSubdir, name)
		if fi, err := a.root.Lstat(rel); err != nil || !goesAsTree(fi.Mode()) {
			continue
		}
		record, err := readGitPlainFileIn(a.root, filepath.Join(rel, "gitdir"))
		if err != nil {
			continue
		}
		if resolveAsFarAsExists(filepath.Dir(strings.TrimSpace(string(record)))) == target {
			a.removeRegistration(name)
			return
		}
	}
}

// removeCreatedRegistration is step B of undoFailedCheckout for a create that
// holds its git directory. registration is the path that the `.git` file of
// the leaf named before step A removed that file. The registration goes when
// it is a direct entry of `worktrees` of the held folder, a real folder, and
// its `gitdir` record names the leaf. The read of the record and the delete go
// through the held root. A delete that fails is not reported, as in the
// Windows step before (removeCreatedRegistration, not measured).
func (a *acceptedGitDir) removeCreatedRegistration(registration, worktreePath string) {
	name, ok := a.registrationName(registration)
	if !ok {
		return
	}
	rel := filepath.Join(worktreesSubdir, name)
	if fi, err := a.root.Lstat(rel); err != nil || !goesAsTree(fi.Mode()) {
		return
	}
	record, err := readGitPlainFileIn(a.root, filepath.Join(rel, "gitdir"))
	if err != nil || !adminRecordNames(registration, record, worktreePath) {
		return
	}
	a.removeRegistration(name)
}

// installIndex copies the index that the checkout wrote at src into the
// registration at adminDir, as the file `index`, for a create that holds its
// git directory. The rule is that of the Windows step (installWorktreeIndex): a
// temporary file beside the index, renamed into place after a full write, and
// no error to the caller. The temporary file, the rename and the remove of a
// temporary file that was not renamed go through the held root, inside
// `worktrees/<name>`. A registration that is not a direct entry of `worktrees`
// of the held folder, or that is no real folder, gets no index.
func (a *acceptedGitDir) installIndex(src, adminDir string) {
	name, ok := a.registrationName(adminDir)
	if !ok {
		return
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return
	}
	rel := filepath.Join(worktreesSubdir, name)
	if fi, err := a.root.Lstat(rel); err != nil || !goesAsTree(fi.Mode()) {
		return
	}
	var salt [8]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return
	}
	tmp := filepath.Join(rel, "index.tmp-"+hex.EncodeToString(salt[:]))
	f, err := a.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = a.root.Rename(tmp, filepath.Join(rel, "index"))
	}
	if err != nil {
		_ = a.root.Remove(tmp)
	}
}

// registrationsRoot opens the registrations folder at dir for a delete or a
// file write of a create, on Linux and macOS. It is safe on nil.
//
// When dir is the folder `worktrees` of the held git directory (isAt), the
// root is opened through the held folder: a folder that was put at the path of
// the git directory gets nothing. With no entry `worktrees` in the held folder
// nothing is opened, and the error is that of a folder that does not exist.
//
// Two cases are opened by the path (openRegistrationsRoot), as before the
// hold. The first is a create that holds no git directory, or a dir of another
// git directory. The second is a `worktrees` entry of the held folder that is
// a symbolic link: 89cb6289 creates with such a link (rows D-1 and D-3 to D-5,
// Linux VM). The index and the rollback delete keep their identity test there
// (replacedIn, D24). The stale step has none.
func (a *acceptedGitDir) registrationsRoot(dir string) (*os.Root, error) {
	if filepath.Base(dir) != worktreesSubdir || !a.isAt(filepath.Dir(filepath.Clean(dir))) {
		return openRegistrationsRoot(dir)
	}
	fi, err := a.root.Lstat(worktreesSubdir)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return nil, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return openRegistrationsRoot(dir)
	}
	return a.root.OpenRoot(worktreesSubdir)
}
