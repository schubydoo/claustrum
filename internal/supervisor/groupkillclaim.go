package supervisor

import "sync/atomic"

// The states of a groupKillClaim.
const (
	claimFree int32 = iota
	claimGivable
	claimSpent
)

// groupKillClaim is the claim on the one group SIGKILL of an exited launcher, or of the
// open drain of a plain spawn. A kill that can give
// its claim back after a refusal holds it as claimGivable. A client KILL or an escalation
// that went out to an exited launcher's group makes it claimSpent (landed). A mark that the group ended
// or is kept makes it claimSpent, and a give-back then leaves it so (claustrum's own, not
// measured). It takes no lock, because the macOS end runs right after the reap.
type groupKillClaim struct{ state atomic.Int32 }

// Load reports that the claim is taken.
func (c *groupKillClaim) Load() bool { return c.state.Load() != claimFree }

// spent reports a spent claim. A kill under way, or a landed kill of the shutdown wait, does not count.
func (c *groupKillClaim) spent() bool { return c.state.Load() == claimSpent }

// Store marks the group as ended, or with false frees the claim.
func (c *groupKillClaim) Store(taken bool) {
	if taken {
		c.state.Store(claimSpent)
		return
	}
	c.state.Store(claimFree)
}

// tryTake takes a free claim for a kill that gives it back if it is refused.
func (c *groupKillClaim) tryTake() bool { return c.state.CompareAndSwap(claimFree, claimGivable) }

// takeFinal takes a free claim for a kill that never gives it back.
func (c *groupKillClaim) takeFinal() bool { return c.state.CompareAndSwap(claimFree, claimSpent) }

// giveBack frees a claim of tryTake after a refused kill, unless a mark came meanwhile.
func (c *groupKillClaim) giveBack() { c.state.CompareAndSwap(claimGivable, claimFree) }

// landed makes a claim of tryTake spent after its kill went out.
func (c *groupKillClaim) landed() { c.state.CompareAndSwap(claimGivable, claimSpent) }
