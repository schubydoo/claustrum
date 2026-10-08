//go:build windows

package main

import (
	"os"
	"path/filepath"
)

// installWorktreeIndex moves the index that the checkout wrote at src into adminDir,
// the registration of the new worktree, as the file "index". A rename fails across
// file systems, so a copy is the fallback. The copy goes to a temporary file beside
// the index and is renamed into place only after a full write, so a failed copy
// leaves no index rather than a partial one.
//
// It is best-effort and always returns nil: a real read-tree that exits 0 has
// written the index. If the move fails, the worktree has no index, as after
// `worktree add --no-checkout`. The index file of 89cb6289 on Windows is not
// measured, so Windows keeps this move. Linux and macOS make a new file
// (githarden_index_unix.go).
func installWorktreeIndex(src, adminDir string) error {
	dst := filepath.Join(adminDir, "index")
	if indexRename(src, dst) == nil {
		return nil
	}
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

// installTestedWorktreeIndex is installWorktreeIndex into tested.path. No create
// reaches it on Windows: the registration tests do not run there, so no
// registration is accepted (acceptRegistration).
func installTestedWorktreeIndex(src string, tested testedRegistration) error {
	return installWorktreeIndex(src, tested.path)
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
func removeCreatedRegistration(reg createdRegistration) error {
	if reg.resolved != "" {
		_ = os.RemoveAll(reg.resolved)
	}
	return nil
}
