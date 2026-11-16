package main

// launcherCheckLogsRepeats is false on macOS: a refusal with the reason of the last
// answer to a launcherCheck logs no line (4c111e86, measured on macOS).
const launcherCheckLogsRepeats = false

// launcherAnswerResetsCheck is true on macOS: an answer with no launcherCheck, usable
// or unusable, makes the next refusal of a launcherCheck log again. That fits the
// rows of 4c111e86 on macOS, but each such answer there followed a change of the
// managed settings, so a settings change alone can be the cause. A -32602 reply to a
// launcherCheck does not end the run (measured on macOS). A none or unreadable answer
// ends it too (claustrum's own, not measured).
const launcherAnswerResetsCheck = true
