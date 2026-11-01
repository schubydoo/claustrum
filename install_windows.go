//go:build windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// cliExeSuffix is the suffix that installCLIPath adds to the version. On Windows
// the CLI file is <cli-dir>\<version>.exe (row W01, Windows VM, 89cb6289). The
// suffix is added always, so -cli-version 9.9.9.exe gives 9.9.9.exe.exe (row
// W06). A var only so a test on any system can run the Windows name rule.
var cliExeSuffix = ".exe"

// probeCLIPathMissing reports that no file exists at exactly path. -probe-cli
// then answers __CLI_BAD__ and starts nothing. Measured on a Windows VM against
// 89cb6289 (rows W08a and W08d): with only 9.9.9.exe on disk, `-probe-cli
// <dir>\9.9.9` prints __CLI_BAD__ in 10 ms and the stub does not run. Without
// this test the process start adds the extension and runs 9.9.9.exe.
//
// Not measured: the exact test that the reference makes, and a folder, a relative
// path or a path with another extension. claustrum tests only that the path
// exists, and leaves every other case to the process start.
func probeCLIPathMissing(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// renameDenied reports a rename of the new CLI that access denied stopped. On
// 5fd08069 a running old CLI gives "Access is denied." (cells W1 and W2,
// Windows VM). placeStagedCLI tries such a rename again.
func renameDenied(err error) bool { return errors.Is(err, syscall.ERROR_ACCESS_DENIED) }

// rawCLIDirStep is off on Windows, so -install keeps its folder step on a cache
// miss only. 5fd08069 makes nothing for C:\D\sub\.. there, also with sub a file
// (cells W-HS10b and W-V1 of round tier3-01, Windows VM).
const rawCLIDirStep = false

// diskFullErrors are the error numbers of a full disk. be1c1ca5 answers
// "There is not enough space on the disk." for a full disk and for an NTFS
// user quota (measured on Windows). That is the text of ERROR_DISK_FULL.
var diskFullErrors = []error{windows.ERROR_DISK_FULL}

// isDiskFull reports an install error that a full disk caused.
func isDiskFull(err error) bool {
	for _, e := range diskFullErrors {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
