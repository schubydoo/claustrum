//go:build unix

package git

// lookCannotAccess is a seam on Linux and macOS, where pathLookRefusal does not
// run outside the tests. It is false there.
var lookCannotAccess = func(error) bool { return false }
