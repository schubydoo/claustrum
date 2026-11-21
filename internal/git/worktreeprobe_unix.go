//go:build unix

package git

import "runtime"

// createAsksAfterAdd is true on Linux and macOS: a create asks git about the
// repository again after its `worktree add` (askAfterAdd), or before its
// content check after the add of a commit id (contentCheck.ask). It is a
// variable so that a test can switch the step off.
var createAsksAfterAdd = true

// createRechecksAfterAdd is false on Linux and macOS, where askAfterAdd makes
// the call after the add.
var createRechecksAfterAdd = false

// createGateAsksAfterAdd is true on macOS: a later gate of a create at its
// checkout, after its add, asks git about the repository after the start test
// and before the read of the config. A good answer is kept, so askAfterAdd
// then makes no call. 4c111e86 does so after a hold that ends at the add
// (measured on macOS). Linux keeps the call of askAfterAdd after the read
// (not measured on 4c111e86). A bad answer there is claustrum's own, not
// measured. It is a variable so that a test can switch the step on.
var createGateAsksAfterAdd = runtime.GOOS == "darwin"

// attachAddListsTwice is true on macOS: a create in attach mode runs one more
// listing in the form of the add right before the listing of the add. 4c111e86
// does so (measured on macOS, in a create after a git.info). Linux keeps one
// listing: in a failed attach add with its fallback, 4c111e86 makes as many
// `config -z` calls as claustrum (measured on Linux). It is a variable so that a
// test can switch the step on.
var attachAddListsTwice = runtime.GOOS == "darwin"
