package main

import "sync/atomic"

// gitCall is the state of one request of a git method. handleGit makes one
// for each request, so no two requests share it.
type gitCall struct {
	// request is the wire request.
	*request
	// shadow is the settings control of the daemon that serves the request
	// (gitshadow.go). handleGit sets it. It is nil for a server with no run folder.
	shadow *shadowHost
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
	// answer of git about the repository (shadowHost.askGit).
	gitRefused bool
	// configRefused is true when the last settings gate of the request refused
	// the config of the repository or another file of its git directory
	// (repoConfigGate).
	configRefused bool
	// stop holds the time limits of the git calls of the request (gitstop.go). It
	// is nil for a method with no such limits.
	stop *gitStop
	// offHold counts the git requests of the daemon with "shadowDirOff":true
	// that run now (handleGit), or is nil. offOwn is the member of this
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
