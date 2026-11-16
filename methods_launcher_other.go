//go:build !linux

package main

// launcherCheckLogsRepeats is true off Linux: each refusal of a launcherCheck logs
// its line, a repeat too. macOS follows c9125eb8 (measured). Windows keeps the same
// rule (claustrum's own, not measured).
const launcherCheckLogsRepeats = true
