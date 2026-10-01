package main

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
// macOS and Windows VMs show the feature at that place. The array is always emitted.
var capabilityFeatures = append(append([]string{
	"process.stdin.offset",
	"git.status.baseRepo",
	"git.info.discovered_root",
	"git.worktree_create.timeoutMs",
	"git.worktree_create.existingBranch",
	"git.worktree_remove.unpushedGuard",
	"process.spawn.shellAgentSocket",
	"launcher.managed",
}, externalRootCapabilityFeatures...), "server.instance_id")

func (s *server) handleServer(c *conn, req *request) *response {
	switch req.Method {
	case "server.ping":
		return ptr(okResult(req.ID, pongResult{Pong: true}))
	case "server.capabilities":
		return ptr(okResult(req.ID, capabilitiesResult{
			Version:    Version,
			Methods:    capabilityMethods,
			InstanceID: s.instanceID,
			StartedAt:  s.startedAt,
			Features:   capabilityFeatures,
		}))
	case methodShutdown:
		// The reply is {"ok":true}, but it reaches the client only when its write
		// wins a race with the teardown. signalShutdown fires here. The handler
		// then waits until dropConns starts (awaitDropStart, bounded), and only
		// then returns the reply for handleRequest to write. docs/PROTOCOL.md
		// (server.shutdown) records the reference measurements. Under a handler
		// panic no frame is emitted for shutdown (the recover in handleRequest
		// skips the error frame).
		//
		// The two log lines come before the teardown starts, so they precede
		// every connection close. The reference logged them in this order before
		// its connection close (measured).
		logInfof("[ServerHandler] server.shutdown received over RPC")
		logInfof("[Server] shutdown requested")
		s.signalShutdown()
		s.awaitDropStart()
		return ptr(okResult(req.ID, shutdownResult{OK: true}))
	default:
		return ptr(unknownMethod(req))
	}
}
