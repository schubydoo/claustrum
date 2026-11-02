//go:build !linux

package files

// ReadRealAvailable is always true off Linux. Windows lists the feature with
// no test (683cc6a9). On macOS always true is claustrum's own: a failing test
// there is not measured.
func ReadRealAvailable() bool { return true }

// LogReadRealStart writes nothing off Linux: the start lines are measured
// on Linux only.
func LogReadRealStart() {}
