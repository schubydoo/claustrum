package main

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
	"time"
)

// writeConfigFileIn writes text as the new file `config.worktree` of entry,
// the root at the registration, on Windows (createRegistrationFile). Then its
// access and write times get written, the write time of the file of the
// daemon after the `--unset-all` calls.
//
// On 5fd08069 a folder there, empty or with a file, gives `openat
// config.worktree: is a directory` (cells S9 and S9b of round task087-01, and
// C6 of round task007-02, Windows VM). A denied create gives `openat
// config.worktree: Access is denied.` (cell S15). A file there gets the new
// text (cell C4 of round task007-01). The write time and the access time are
// equal, 0.5 to 2.3 ms before the creation time (cell S10, 5 runs).
func writeConfigFileIn(entry *os.Root, text []byte, written time.Time) error {
	f, err := createRegistrationFile(entry, worktreeConfigName)
	if err != nil {
		return err
	}
	_, err = f.Write(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil || written.IsZero() {
		return err
	}
	return entry.Chtimes(worktreeConfigName, written, written)
}

// createRegistrationFile makes name as a new file in entry, the root at the
// registration, on Windows. No open goes to a file that is there, so a hard
// link there never gets the new bytes.
//
//   - A folder at name, empty or not, fails with `openat <name>: is a
//     directory`, the text of 5fd08069 (cells S9, S9b, C6 and S12).
//   - Any other entry there goes first, with one plain remove, never as a
//     tree. A link goes as a link.
//   - Then the file is made with O_EXCL. If an entry is there again, it goes
//     with one more plain remove, and the create runs one more time. The
//     second try and the remove before the first try are claustrum's own
//     (not measured).
func createRegistrationFile(entry *os.Root, name string) (*os.File, error) {
	remove := func() error {
		if err := entry.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if fi, err := entry.Lstat(name); err == nil {
		if fi.IsDir() {
			return nil, &os.PathError{Op: "openat", Path: name, Err: syscall.EISDIR}
		}
		if err := remove(); err != nil {
			return nil, err
		}
	}
	create := func() (*os.File, error) {
		return entry.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	}
	f, err := create()
	if errors.Is(err, fs.ErrExist) {
		if err := remove(); err != nil {
			return nil, err
		}
		f, err = create()
	}
	return f, err
}
