package git

// createAsksAfterAdd is false on Windows (askAfterAdd). A bad answer there
// fails no create, and the content check asks no question (claustrum's own,
// not measured). It is a variable so that a test can switch the step on.
var createAsksAfterAdd = false

// createRechecksAfterAdd is true on Windows: a create of a branch asks git
// about the repository right after its `worktree add`, before the call that
// resolves the branch to a commit. 4c111e86 makes that call there in a first
// and in a second create (measured on Windows). The request goes on after any
// answer (recheck). That rule for a bad answer is claustrum's own, not
// measured.
var createRechecksAfterAdd = true

// createGateAsksAfterAdd and attachAddListsTwice are false on Windows
// (claustrum's own, not measured).
var (
	createGateAsksAfterAdd = false
	attachAddListsTwice    = false
)
