package main

// launcherCheckLogsRepeats is false on Linux: a refusal with the reason of the last
// answer to a launcherCheck logs no line (4c111e86, measured on Linux).
const launcherCheckLogsRepeats = false

// launcherAnswerResetsCheck is false on Linux: an answer with no launcherCheck
// leaves the key of noteChecked (claustrum's own, not measured on Linux).
const launcherAnswerResetsCheck = false
