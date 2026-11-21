package main

// launcherCheckAnyMembers is false on macOS: a launcherCheck member of another JSON
// type gets Invalid params (claustrum's own, not measured).
const launcherCheckAnyMembers = false

// launcherAnswerResetsCheck is true on macOS: an answer with no launcherCheck, usable
// or unusable, makes the next refusal of a launcherCheck log again. A rewrite of the
// managed settings with no such answer does not (4c111e86, measured on macOS).
// A -32602 reply to a launcherCheck does not end the run (measured on macOS). A none
// or unreadable answer ends it too (claustrum's own, not measured).
const launcherAnswerResetsCheck = true
