package main

// launcherCheckAnyMembers is false on Linux: a launcherCheck member of another JSON
// type gets Invalid params (claustrum's own, not measured).
const launcherCheckAnyMembers = false

// launcherAnswerResetsCheck is false on Linux: an answer with no launcherCheck
// leaves the key of noteChecked (claustrum's own, not measured on Linux).
const launcherAnswerResetsCheck = false
