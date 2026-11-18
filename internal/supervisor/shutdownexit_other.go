//go:build !linux && !darwin

package supervisor

// quietEveryShutdownExit is false here: a child that the shutdown sweep ended logs
// its exited line (claustrum's own, not measured).
const quietEveryShutdownExit = false
