//go:build !darwin

package git

// createAsksAfterAdd is false off macOS (askAfterAdd). Windows makes no such
// call (claustrum's own, not measured). On Linux the reference makes its call
// after the checkout, which claustrum does not build. It is a variable so that
// a test can switch the step on.
var createAsksAfterAdd = false
