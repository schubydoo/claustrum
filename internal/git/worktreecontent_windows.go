package git

import "golang.org/x/sys/windows"

// readHeldElsewhere is the error of a read of a file that another process
// holds open with no share mode. The content check passes such a file
// (contentCheck.compare).
var readHeldElsewhere error = windows.ERROR_SHARING_VIOLATION
