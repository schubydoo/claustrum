package main

// stagedEntryTakesWorktreesGroup is false on macOS: the staged entry keeps
// the mode of its mkdir and the group of `.git` (stagedEntryModes).
const stagedEntryTakesWorktreesGroup = false
