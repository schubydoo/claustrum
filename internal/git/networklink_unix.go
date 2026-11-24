//go:build unix

package git

// linkTarget reads no reparse data on Linux and macOS: the test of
// networkLinkRefusal runs on Windows only.
func linkTarget(string) (string, bool) { return "", false }

// linkRelative has no link to read on Linux and macOS.
func linkRelative(string) bool { return false }

// networkAccessDenied is false on Linux and macOS, where no share is looked at.
func networkAccessDenied(error) bool { return false }
