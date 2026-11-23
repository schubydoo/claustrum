package supervisor

import "time"

// groupEndAfterReap is true: macOS ends the group right after the reap of the leader
// (87375cc7, measured on macOS).
const groupEndAfterReap = true

// settleLeftGroupHeld is never called on macOS.
func (p *Proc) settleLeftGroupHeld(<-chan struct{}) time.Time { return time.Time{} }
