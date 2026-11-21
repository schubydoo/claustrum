//go:build !linux && !darwin

package main

// launcherCheckAnyMembers is true on Windows: any launcherCheck object is unusable
// with windows_host. The shapes on the launcher.resolve page are measured on Windows
// (4c111e86), such as "path":5. Other member values are claustrum's own, not measured.
const launcherCheckAnyMembers = true

// launcherAnswerResetsCheck is true on Windows: an answer with no launcherCheck, of
// status none, makes the next equal refusal of a launcherCheck log again. A -32602
// "launcherCheck is required with path" between them does not (4c111e86, measured
// on Windows). A usable or unreadable answer ends the run too (claustrum's own, not
// measured).
const launcherAnswerResetsCheck = true
