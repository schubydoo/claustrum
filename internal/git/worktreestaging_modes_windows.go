package git

import "os"

// stagedEntryModes does nothing on Windows: the staged files keep the modes
// of their create (copyRegistrationTree).
func stagedEntryModes(_, _ *os.Root, _ string) {}

// stagingFolderMode is the mode of the mkdir of the staging folder.
const stagingFolderMode = 0o755

// mainRefsOpenTest is true on Windows: the staged add opens `reftable\tables.list`
// of the git directory (mainRefsFault). A variable, so that a test on another
// system can run that step.
var mainRefsOpenTest = true
