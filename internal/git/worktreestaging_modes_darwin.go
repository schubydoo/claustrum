package git

// stagedEntryTakesWorktreesGroup is false on macOS: the staged entry keeps
// the mode of its mkdir and the group of `.git` (stagedEntryModes).
const stagedEntryTakesWorktreesGroup = false

// stagingFolderMode is the mode of the mkdir of the staging folder, before
// the umask. On macOS b3081b51 gives the new entry 755 under umask 022 and
// 775 under umask 002 (measured on macOS). Other umasks are not measured.
const stagingFolderMode = 0o775
