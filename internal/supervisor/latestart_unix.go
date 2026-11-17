//go:build unix

package supervisor

// lateStartEnds tells if a spawn ends the child of a start that returned after the
// shutdown began. On Linux and macOS it does not. The child runs on with no start
// line, no table entry and no record. 4c111e86, measured on Linux under a flood of
// process.spawn: in each run the count of ended groups equaled the count of start
// lines, and some runs left children running after the exit. macOS is claustrum's own.
const lateStartEnds = false
