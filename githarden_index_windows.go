//go:build windows

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// installWorktreeIndex copies the index that the checkout wrote at src into
// adminDir, the registration of the new worktree, as the file "index". The copy
// goes to a temporary file beside the index. It is renamed into place only
// after a full write. So a failed copy leaves no index, not a partial one.
//
// The file at src stays. The cell is V5i of val-s3p2b2-5-windows: a create with
// a `.worktreeinclude` file, 4 runs per side, Windows VM. On 5fd08069 the file
// `<S>\.index-<n>\index` (297 bytes) is there from the read-tree to the reply.
// At the end `.git\worktrees\w1\index` has 297 bytes. The build before moved
// the file: it was gone when the index of the registration appeared. The cell
// has no member. With the member the file of 5fd08069 is not measured: it gets
// the same copy. The name of the temporary file is claustrum's own.
//
// It is best-effort and always returns nil: a real read-tree that exits 0 has
// written the index. If the copy fails, the worktree has no index, as after
// `worktree add --no-checkout`. Linux and macOS make a new file too
// (githarden_index_unix.go).
//
// A create that holds its git directory does not come here: its placement
// goes through the held folder (acceptedGitDir.installIndex). This placement
// by the path is that of a request with "shadowDirOff":true, of the state
// fallback-git and of a server with no run folder.
func installWorktreeIndex(src, adminDir string) error {
	dst := filepath.Join(adminDir, "index")
	b, err := os.ReadFile(src)
	if err != nil {
		return nil
	}
	tmp, err := os.CreateTemp(adminDir, "index.tmp-")
	if err != nil {
		return nil
	}
	err = indexWrite(tmp, b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = indexRename(tmp.Name(), dst)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return nil
}

// installTestedWorktreeIndex writes the index that the checkout wrote at src
// into tested, the registration that the staging step moved into place
// (gitUse.stagedRegistration). The file `index` is a new file of the
// registration, through the held git directory and a root at it, with no
// temporary file, no rename and no rename retry (createRegistrationFile). Then its
// access and write times get the write time of src.
// The registration must still be the folder of the move (replacedIn). Each
// error goes back, and the create answers it as a failed checkout.
//
// On 5fd08069 a folder `index` gives `git worktree add failed (checkout):
// openat index: is a directory` (cell S12 of round task087-01, Windows VM). A
// denied create gives `openat index: Access is denied.` (cell S11, and X6w of
// round task009-01). The write time and the access time are equal, 7.8 to 9.1
// ms before the creation time (cell S13, 5 runs). The text of a replaced
// registration is claustrum's own. A registration with no held git directory
// gets installWorktreeIndex (not measured).
func installTestedWorktreeIndex(src string, tested testedRegistration) error {
	if tested.git == nil {
		return installWorktreeIndex(src, tested.path)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	root, err := tested.git.registrationsRoot(filepath.Dir(tested.path))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(tested.path)
	if tested.replacedIn(root, name) {
		return errors.New("the registration " + tested.path + " is not the folder that was moved into place")
	}
	entry, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer func() { _ = entry.Close() }()
	out, err := createRegistrationFile(entry, "index")
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return entry.Chtimes("index", st.ModTime(), st.ModTime())
}

// indexRename and indexWrite are seams for the tests of installWorktreeIndex.
var (
	indexRename = os.Rename
	indexWrite  = func(f *os.File, b []byte) error { _, err := f.Write(b); return err }
)

// removeCreatedRegistration deletes the registration that createdWorktreeAdminDir
// verified, by its resolved path. It is step B of undoFailedCheckout. On Windows a failed
// delete is not reported, so it always returns nil: the rollback of 89cb6289 with a
// registration that stays is not measured there. The delete uses no root, so it
// has neither the direct child test nor the identity test of Linux and macOS.
// A create that holds its git directory does not come here
// (acceptedGitDir.removeCreatedRegistration).
func removeCreatedRegistration(reg createdRegistration) error {
	if reg.resolved != "" {
		_ = os.RemoveAll(reg.resolved)
	}
	return nil
}
