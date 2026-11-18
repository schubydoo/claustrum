//go:build unix

package supervisor

// lateStartEnds tells if a spawn ends the child of a start that returned after the
// shutdown began. On Linux and macOS it does not, and the child runs on with no start
// line, no table entry and no record. That rule is claustrum's own. 4c111e86, measured
// on Linux under a flood of process.spawn: in each run the count of ended groups
// equaled the count of start lines, and some runs left children with no start line
// running after the exit. The row does not show if their starts had returned.
var lateStartEnds = false
