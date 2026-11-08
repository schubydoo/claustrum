package git

// createAsksAfterAdd is false on Windows (askAfterAdd). Windows makes no such
// call (claustrum's own, not measured). It is a variable so that a test can
// switch the step on.
var createAsksAfterAdd = false
