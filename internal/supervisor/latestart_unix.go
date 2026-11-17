//go:build unix

package supervisor

// lateStartEnds tells if a spawn ends the child of a start that returned after the
// shutdown began. On Linux and macOS it does not. The child runs on with no start
// line, no table entry and no record (4c111e86, measured on Linux under a flood of
// process.spawn: every child with a start line ended, and children with none ran on).
const lateStartEnds = false
