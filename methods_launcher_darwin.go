package main

// launcherCheckLogsRepeats is false on macOS: a refusal with the reason of the last
// answer to a launcherCheck logs no line (4c111e86, measured on macOS).
const launcherCheckLogsRepeats = false

// launcherAnswerResetsCheck is true on macOS: an answer with no launcherCheck, usable
// or unusable, makes the next refusal of a launcherCheck log again. A -32602 reply
// does not (4c111e86, measured on macOS). A none or unreadable answer does the same
// (claustrum's own, not measured).
const launcherAnswerResetsCheck = true
