//go:build windows

package main

import (
	"errors"
	"io/fs"
	"os"
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
