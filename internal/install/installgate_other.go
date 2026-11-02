//go:build !linux

package install

// cliFileLooksRunnable is the Linux file test of a cache hit with the managed
// launcher gate on. On macOS and Windows the run of the CLI decides, as
// before. Those systems are not measured.
func cliFileLooksRunnable(string) bool { return true }
