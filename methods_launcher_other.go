//go:build !linux && !darwin

package main

// launcherCheckAnyMembers is true on Windows: any launcherCheck object is unusable
// with windows_host, whatever its members hold (4c111e86, measured on Windows).
const launcherCheckAnyMembers = true

// launcherAnswerResetsCheck is false on Windows (claustrum's own, not measured).
const launcherAnswerResetsCheck = false
