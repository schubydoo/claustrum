//go:build windows

package supervisor

// lateStartEnds tells if a spawn ends the child of a start that returned after the
// shutdown began. On Windows it does (claustrum's own, not measured for this step).
var lateStartEnds = true
