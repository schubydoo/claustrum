//go:build unix

package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
)

// readGitPlainFile reads the file at path. It is the read of the daemon itself for a
// `.git` file, a `commondir` file and a `gitdir` record. The open does not block, so
// a FIFO with no writer is judged at once. Anything that is not a regular file is an
// error, and so is a file of more than gitFileMaxBytes bytes. Each caller takes the
// error as "no usable file here". 89cb6289 answers in under 2 s with a FIFO in those
// places (rows B-F1, B-F4, B-S2, B-G3 and B-L1 on Linux and macOS VMs).
//
// Not measured: a device or a socket in place of the FIFO, a FIFO that a writer
// opens during the request, and a file larger than a normal `.git` file. The size
// bound is claustrum's choice.
func readGitPlainFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return readOpenedPlainFile(f)
}

// readGitPlainFileIn is readGitPlainFile for name inside root. The rules of os.Root
// for a symbolic link stay as they are.
func readGitPlainFileIn(root *os.Root, name string) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return readOpenedPlainFile(f)
}

// readOpenedPlainFile reads f and closes it. The test for a regular file is on the
// open handle, so a swap of the path after the open changes nothing.
func readOpenedPlainFile(f *os.File) ([]byte, error) {
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "read", Path: f.Name(), Err: errNotRegularFile}
	}
	b, err := io.ReadAll(io.LimitReader(f, gitFileMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > gitFileMaxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", f.Name(), gitFileMaxBytes)
	}
	return b, nil
}
