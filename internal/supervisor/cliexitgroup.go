package supervisor

import "path/filepath"

// cliExitEndsGroup reports a spawn with no launcher whose group the exit goroutine ends
// as it ends the group of a launcher: a command named ccd-cli, or one in a folder named
// ccd-cli, with no pty object. 13047a51 ends what such a child leaves in its group
// (measured on Linux for <dir>/ccd-cli/2.0.0, on Linux and macOS for <dir>/ccd-cli). A
// pty spawn of 13047a51 ends by another rule (measured on macOS), and this end skips it.
// Other names and the gitRead spawn are claustrum's own, not measured.
func cliExitEndsGroup(command string, tempRoot TempRootAsk, gitRead bool) bool {
	if tempRoot.Pty || gitRead {
		return false
	}
	return filepath.Base(command) == "ccd-cli" || filepath.Base(filepath.Dir(command)) == "ccd-cli"
}
