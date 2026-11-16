//go:build !linux && !darwin

package main

// launcherCheckLogsRepeats is true on Windows: each refusal of a launcherCheck logs
// its line, a repeat too (claustrum's own, not measured).
const launcherCheckLogsRepeats = true

// launcherAnswerResetsCheck is false on Windows, where each refusal logs anyway
// (claustrum's own, not measured).
const launcherAnswerResetsCheck = false
