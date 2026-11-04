package git

// stagedEntryTakesWorktreesGroup is true on Linux: the staged entry takes the
// mode of its source folder and the group of a set-group-ID `worktrees`
// (stagedEntryModes).
const stagedEntryTakesWorktreesGroup = true

// stagingFolderMode is the mode of the mkdir of the staging folder, before
// the umask. stagedEntryModes then gives it the mode of its source folder.
const stagingFolderMode = 0o755
