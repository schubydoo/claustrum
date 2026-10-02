//go:build !windows

package main

// cliExeSuffix is the suffix that installCLIPath adds to the version. It is empty
// off Windows: the CLI file is <cli-dir>/<version> (Linux and macOS VMs,
// 89cb6289). A var only so a test on any system can run the Windows name rule.
var cliExeSuffix = ""

// probeCLIPathMissing is the Windows-only file test of -probe-cli. Off Windows
// the process start decides, as before.
func probeCLIPathMissing(string) bool { return false }
