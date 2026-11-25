package git

// stagedEntryTakesWorktreesGroup is true on Linux: the staged entry takes the
// mode of its source folder and the group of a set-group-ID `worktrees`
// (stagedEntryModes).
const stagedEntryTakesWorktreesGroup = true

// stagingFolderMode is the mode of the mkdir of the staging folder, before
// the umask. 87375cc7 asks for 0777 (measured on Linux under umask 022, 000
// and 077). stagedEntryModes then gives it the mode of its source folder.
const stagingFolderMode = 0o777

// addStageTestsWorktrees is true on Linux: openAddStage makes no `worktrees`
// folder that is there already.
const addStageTestsWorktrees = true
