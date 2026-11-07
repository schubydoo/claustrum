//go:build !windows

package supervisor

// adoptTakesLoginPATH is true on Linux and macOS: after a good read of the
// login-shell PATH a child of an adopted set gets that PATH, as in process.spawn,
// not the sent one (c9125eb8, measured on Linux and macOS). Before a good read the
// sent PATH stays (claustrum's own, not measured).
const adoptTakesLoginPATH = true

// envKey is the name itself: names are case-sensitive here.
func envKey(name string) string { return name }

// keptOnlyNames is empty on Linux and macOS: a sent HOME reaches the child
// (c9125eb8, measured on Linux, claustrum's own on macOS, not measured).
var keptOnlyNames []string
