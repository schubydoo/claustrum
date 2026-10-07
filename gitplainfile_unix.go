//go:build unix

package main

import (
	"io"
	"io/fs"
	"os"
	"syscall"
)

// readGitPlainFile reads the file at path. It is the read of the daemon itself for a
// `.git` file, a `commondir` file and a `gitdir` record. The open does not block, so
// a FIFO with no writer is judged at once. Anything that is not a regular file is an
// error. Each caller takes the error as "no usable file here". 89cb6289 answers in
// under 2 s with a FIFO in those places (rows B-F1, B-F4, B-S2, B-G3 and B-L1 on
// Linux and macOS VMs). A regular file of any size is read whole, as os.ReadFile
// reads it.
//
// Not measured: a device or a socket as a `commondir` file or a `gitdir` record, and
// a FIFO that a writer opens during the request.
func readGitPlainFile(path string) ([]byte, error) {
	f, err := openGitPlainFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// readGitPlainFileIn is readGitPlainFile for name inside root. The rules of os.Root
// for a symbolic link stay as they are.
func readGitPlainFileIn(root *os.Root, name string) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := requireRegularFile(f); err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// openGitPlainFile opens the regular file at path for a read. The open does not
// block. Anything that is not a regular file is an error, and no handle stays open.
func openGitPlainFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := requireRegularFile(f); err != nil {
		return nil, err
	}
	return f, nil
}

// requireRegularFile closes f and returns an error when f is not a regular file. The
// test is on the open handle, so a swap of the path after the open changes nothing.
func requireRegularFile(f *os.File) error {
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = &fs.PathError{Op: "read", Path: f.Name(), Err: errNotRegularFile}
	}
	if err != nil {
		_ = f.Close()
	}
	return err
}
