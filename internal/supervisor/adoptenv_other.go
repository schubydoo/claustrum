//go:build !windows

package supervisor

// osKeptDaemonNames is empty on Linux and macOS: after an adopt the HOME, USER,
// LOGNAME and LANG of the daemon are gone from a child (c9125eb8, measured on
// macOS).
var osKeptDaemonNames []string

// adoptTakesLoginPATH is true on Linux and macOS: after a good read of the
// login-shell PATH a child of an adopted set gets that PATH, as in process.spawn,
// not the sent one (c9125eb8, measured on Linux and macOS). Before a good read the
// sent PATH stays (claustrum's own, not measured).
const adoptTakesLoginPATH = true

// envKey is the name itself: names are case-sensitive here.
func envKey(name string) string { return name }

// osAdoptedBase adds nothing on Linux and macOS.
func osAdoptedBase(base []string) []string { return base }
