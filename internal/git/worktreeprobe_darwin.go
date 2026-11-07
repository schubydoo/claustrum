//go:build darwin

package git

// createAsksAfterAdd is true on macOS: a create asks git about the repository
// again after its `worktree add` (askAfterAdd). It is a variable so that a test
// can switch the step off.
var createAsksAfterAdd = true
