package git

import (
	"sync/atomic"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// Deps is the daemon state that the git methods read. Both fields point at
// objects that the server owns for its whole life, so every request shares them.
type Deps struct {
	// Shadow is the settings control of the daemon, the field gitShadow of the
	// server. It is nil for a server with no run folder.
	Shadow *ShadowHost
	// OffRequests counts the git requests with "shadowDirOff":true that have not
	// answered yet, the field offGitRequests of the server.
	OffRequests *atomic.Int32
}

// gitCall is the state of one request of a git method. Handle makes one
// for each request, so no two requests share it.
type gitCall struct {
	// Request is the wire request.
	*rpc.Request
	// shadow is the settings control of the daemon that serves the request
	// (gitshadow.go). Handle sets it. It is nil for a server with no run folder.
	shadow *ShadowHost
	// repoRead is the accepted read of the repository's config, in the state
	// shadow. settingsGateRefusal sets it.
	repoRead *repoVerdict
	// use is how the git calls of the request name their repository (gitUse).
	// It is the zero value until settingsGateRefusal accepted the repository in
	// the state shadow.
	use gitUse
	// atReply are the removals that wait for the reply of the request
	// (runAtReply).
	atReply []func()
	// gitRefused is true when the last settings gate of the request got a bad
	// answer of git about the repository (ShadowHost.askGit).
	gitRefused bool
	// gitNotARepo is true when that bad answer is repoAnswer.notARepo.
	gitNotARepo bool
	// gitDisagreed is true when that bad answer is repoAnswer.disagree.
	gitDisagreed bool
	// configRefused is true when the last settings gate of the request refused
	// the config of the repository or another file of its git directory
	// (repoConfigGate).
	configRefused bool
	// goneCopyText is the refusal of a failed copy that the last settings gate
	// answered as "no repository" (gitDirTrust.goneEnvGitDir), or "".
	goneCopyText string
	// stop holds the time limits of the git calls of the request (gitstop.go). It
	// is nil for a method with no such limits.
	stop *gitStop
	// offHold counts the git requests of the daemon with "shadowDirOff":true
	// that run now (Handle), or is nil. offOwn is the member of this
	// request (bindGitParams).
	offHold *atomic.Int32
	offOwn  bool
}

// offLive reports whether the off switch holds the daemon now: this request
// or another git request with "shadowDirOff":true runs. On 5fd08069 the hold
// changes the form of the git commands while a request runs (measured on
// Linux).
func (req *gitCall) offLive() bool {
	return req.offOwn || req.offHold != nil && req.offHold.Load() > 0
}

// runAtReply removes the short-lived folders that stay until the reply. On
// 5fd08069 the folder `.index-<n>` of a create goes at the reply, also with an
// include step and after a failed read-tree (measured on Linux, macOS and Windows).
func (req *gitCall) runAtReply() {
	for _, remove := range req.atReply {
		remove()
	}
	req.atReply = nil
}
