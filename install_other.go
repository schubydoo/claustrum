//go:build !windows

package main

// cliExeSuffix is the suffix that installCLIPath adds to the version. It is empty
// off Windows: the CLI file is <cli-dir>/<version> (Linux and macOS VMs,
// 89cb6289). A var only so a test on any system can run the Windows name rule.
var cliExeSuffix = ""

// probeCLIPathMissing is the Windows-only file test of -probe-cli. Off Windows
// the process start decides, as before.
func probeCLIPathMissing(string) bool { return false }

// renameDenied is false off Windows: the rename of the new CLI runs once.
func renameDenied(error) bool { return false }

// rawCLIDirStep turns on the folder step of makeCLIDir. Linux and macOS make the
// folders of the raw -cli-dir text (cells L-HS10b, L-V1, L-V2, L-V3 and M-HS10b of round tier3-01).
const rawCLIDirStep = true
