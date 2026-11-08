//go:build unix

package git

// createAsksAfterAdd is true on Linux and macOS: a create asks git about the
// repository again after its `worktree add` (askAfterAdd), or before its
// content check after the add of a commit id (contentCheck.ask). It is a
// variable so that a test can switch the step off.
var createAsksAfterAdd = true
