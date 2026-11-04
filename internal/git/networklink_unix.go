//go:build unix

package git

// linkTarget reads no reparse data on Linux and macOS: the test of
// networkLinkRefusal runs on Windows only.
func linkTarget(string) (string, bool) { return "", false }
