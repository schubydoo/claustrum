//go:build !windows

package git

// readHeldElsewhere is nil off Windows: no read error passes the content
// check there (contentCheck.compare).
var readHeldElsewhere error
