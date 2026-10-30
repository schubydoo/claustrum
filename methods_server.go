package main

import "slices"

var capabilityMethods = []string{
	"server.ping", "server.capabilities", methodShutdown,
	"files.list", "files.validate", "files.stat", "files.read", "files.extract_tar",
	"git.info", "git.status", "git.list_branches", "git.worktree_create", "git.worktree_remove",
	// launcher.resolve (89cb6289) sits between git.worktree_remove and process.spawn
	// on Linux, macOS and Windows (VM-measured). It takes the method count to 20.
	"launcher.resolve",
	"process.spawn", "process.stdin", "process.kill", "process.killAndWait", "process.reattach",
	// plugins.prune (19f30c46) is appended last.
	"plugins.prune",
}

// capabilityFeatures advertises optional protocol extensions, in the reference's
// order. process.stdin.offset (the stdin-offset idempotency contract, see
// process.stdin / stdinResult) landed in 7c2f88d; 7d193f89 added two more:
// git.status.baseRepo (git.status now keys off a session worktree of baseRepo)
// and git.worktree.external_root (a worktreeRoot param places the session worktree
// OUTSIDE the repo, under that caller-chosen root, instead of inside it). external_root
// is unix-only: the reference gates that capability off on Windows and drops the feature
// from its Windows features list (externalRootCapabilityFeatures is OS-split). 4534d86
// inserted git.worktree_create.timeoutMs before external_root (a caller-supplied
// per-request deadline on the worktree add + checkout), present on every OS, and
// appended server.instance_id (the capabilities reply now carries a per-boot instanceId),
// always last and on every OS. 19f30c46 inserted git.worktree_create.existingBranch
// after timeoutMs and before external_root (worktree_create can attach an
// already-existing branch), present on every OS like timeoutMs. f6010b97 inserted
// process.spawn.shellAgentSocket after existingBranch and before external_root
// (process.spawn hands a child the login shell's SSH_AUTH_SOCK, and takes a
// disableShellAgentSocket param), present on every OS. claustrum's Windows
// spawn never probes. 89cb6289 inserted git.worktree_remove.unpushedGuard after
// existingBranch and before shellAgentSocket. git.worktree_remove then keeps a branch
// that no other ref reaches, and answers branchKept. Linux, macOS and Windows VMs
// show the feature. 89cb6289 also inserted launcher.managed after shellAgentSocket
// and before external_root (launcher.resolve and the process.spawn launcher param).
// Windows lists it too, although a Windows spawn refuses every launcher. 89cb6289
// also inserted git.info.discovered_root after git.status.baseRepo. In that build
// git.info runs no `rev-parse --show-toplevel` on Linux and macOS (row L01). Linux,
// macOS and Windows VMs show the feature at that place. 5fd08069 inserted
// server.peer_check after launcher.managed (the peerCheck member of this result).
// Linux, macOS and Windows VMs show it there. 5fd08069 also inserted
// files.extract_tar.execBit after process.stdin.offset (files.extract_tar keeps the
// execute bit of the owner as mode 0700). The same three VMs show it at that place. 5fd08069
// also lists git.shadow_dir after server.peer_check (Linux, macOS and Windows VMs).
// 52d82f0a inserted process.kill.farewell after process.stdin.offset,
// files.list.includeDotNames after files.extract_tar.execBit, and
// process.spawn.failure_reason and server.line_end after shellAgentSocket (Linux,
// macOS and Windows VMs). The array is always emitted.
var capabilityFeatures = append(append([]string{
	"process.stdin.offset",
	"process.kill.farewell",
	"files.extract_tar.execBit",
	"files.list.includeDotNames",
	"git.status.baseRepo",
	"git.info.discovered_root",
	"git.worktree_create.timeoutMs",
	"git.worktree_create.existingBranch",
	"git.worktree_remove.unpushedGuard",
	"process.spawn.shellAgentSocket",
	"process.spawn.failure_reason",
	"server.line_end",
	"launcher.managed",
	"server.peer_check",
	"git.shadow_dir",
}, externalRootCapabilityFeatures...), "server.instance_id")

// jobFeature is the feature of a daemon that puts each child in a job.
const jobFeature = "process.spawn.job"

// featuresFor is the features list of this daemon. A daemon that puts each child in a
// job, which only Windows does, adds process.spawn.job after server.instance_id. With
// CLAUDE_SSH_NO_JOBS=1 the name is not in the list (52d82f0a, Windows VM).
func (s *server) featuresFor() []string {
	if s.procs == nil || s.procs.jobs != jobsOn {
		return capabilityFeatures
	}
	return append(slices.Clip(capabilityFeatures), jobFeature)
}

func (s *server) handleServer(c *conn, req *request) *response {
	switch req.Method {
	case "server.ping":
		return ptr(okResult(req.ID, pongResult{Pong: true}))
	case "server.capabilities":
		return ptr(okResult(req.ID, capabilitiesResult{
			Version:     Version,
			Methods:     capabilityMethods,
			InstanceID:  s.instanceID,
			StartedAt:   s.startedAt,
			Features:    s.featuresFor(),
			PeerCheck:   s.peerCheckAnswer(),
			PeerCheckBy: s.peerCheckByAnswer(),
		}))
	case methodShutdown:
		// The reply is {"ok":true}, but it reaches the client only when its write
		// wins a race with the teardown. signalShutdown fires here, and the handler
		// returns the reply at once for handleRequest to write. It does not wait
		// for the close of the connections. 5fd08069 answers before the close in
		// part of the runs (see "The reply races the close" on the server.* page
		// under docs/protocol/). Under a handler panic no frame is emitted for
		// shutdown (the recover in handleRequest skips the error frame).
		//
		// The two log lines come before the teardown starts, so they precede
		// every connection close. The reference logged them in this order before
		// its connection close (measured).
		logInfof("[ServerHandler] server.shutdown received over RPC")
		logInfof("[Server] shutdown requested")
		if c != nil {
			// The cleanup count keeps this connection (dropConns).
			s.mu.Lock()
			c.sentStop = true
			s.mu.Unlock()
		}
		s.signalShutdown()
		return ptr(okResult(req.ID, shutdownResult{OK: true}))
	default:
		return ptr(unknownMethod(req))
	}
}
