//go:build !linux

package install

// checkFaultShown is false off Linux. macOS keeps a spawn refusal with no
// launcherCheck free of launcherFault (not measured: no row shows one there).
// Windows refuses every launcher with windows_host before this check.
const checkFaultShown = false
