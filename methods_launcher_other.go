//go:build !linux && !darwin

package main

// launcherCheckAnyMembers is true on Windows: any launcherCheck object is unusable
// with windows_host. The shapes on the launcher.resolve page are measured on Windows
// (4c111e86), such as "path":5. Other member values are claustrum's own, not measured.
const launcherCheckAnyMembers = true

// launcherAnswerResetsCheck is false on Windows (claustrum's own, not measured).
const launcherAnswerResetsCheck = false
