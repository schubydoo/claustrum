//go:build windows

package sessiondirs

import "strings"

// installHint ends the text of NoOtherGitError on Windows (measured).
const installHint = `C:\Program Files\Git`

// openNoBlock is no flag on Windows.
const openNoBlock = 0

// sameEnvName compares two names of the environment in any letter case.
func sameEnvName(a, b string) bool { return strings.EqualFold(a, b) }

// warnBare is false: Windows logs no warning of a bare command (measured).
const warnBare = false

// matchByIdentity is false on Windows: the paths match with no regard to letter
// case (samePath).
const matchByIdentity = false

// samePath compares two paths with no regard to letter case (measured on Windows:
// a lower-case entry is passed over and cut).
func samePath(a, b string) bool { return strings.EqualFold(a, b) }

// folderIdentity is 0 and 0 on Windows, as the records of b3081b51 hold there
// (measured).
func folderIdentity(string) (dev, ino uint64) { return 0, 0 }
