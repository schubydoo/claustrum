//go:build !windows

package install

import "errors"

// cliExeSuffix is the suffix that installCLIPath adds to the version. It is empty
// off Windows: the CLI file is <cli-dir>/<version> (`89cb6289`, measured on Linux
// and macOS). A var only so a test on any system can run the Windows name rule.
var cliExeSuffix = ""

// probeCLIPathMissing is the Windows-only file test of -probe-cli. Off Windows
// the process start decides, as before.
func probeCLIPathMissing(string) bool { return false }

// renameDenied is false off Windows: the rename of the new CLI runs once.
func renameDenied(error) bool { return false }

// rawCLIDirStep turns on the folder step of makeCLIDir. Linux and macOS make the
// folders of the raw -cli-dir text (measured).
const rawCLIDirStep = true

// isDiskFull reports an install error that a full disk caused.
func isDiskFull(err error) bool {
	for _, e := range diskFullErrors {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
