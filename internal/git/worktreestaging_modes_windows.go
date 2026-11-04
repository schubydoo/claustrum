package git

import "os"

// stagedEntryModes does nothing on Windows: the staged files keep the modes
// of their create (copyRegistrationTree).
func stagedEntryModes(_, _ *os.Root, _ string) {}

// stagingFolderMode is the mode of the mkdir of the staging folder.
const stagingFolderMode = 0o755
