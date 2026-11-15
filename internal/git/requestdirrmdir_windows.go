package git

import "os"

// removeEmptyRequestDir removes nothing on Windows. requestDir makes no folder
// by its path there, so this step does not run (from the code).
func removeEmptyRequestDir(_, _ string, _ os.FileInfo) {}
