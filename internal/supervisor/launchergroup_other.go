//go:build !linux && !darwin

package supervisor

import "time"

// LauncherGroupEndBuilt is false: Windows has no end of a launcher's group.
const LauncherGroupEndBuilt = false

// groupEndAfterReap is false here, and no Proc has endsGroup set.
const groupEndAfterReap = false

func (p *Proc) settleLeftGroupHeld(<-chan struct{}) time.Time { return time.Time{} }

func (p *Proc) settleLeftGroupReaped() {}

// killHeldGroupAtStop has no group to end here. The field stays false.
func (p *Proc) killHeldGroupAtStop() bool { return p.stopKilledGroup }
