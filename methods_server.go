package main

var capabilityMethods = []string{
	"server.ping", "server.capabilities", methodShutdown,
	"files.list", "files.validate", "files.stat", "files.read", "files.extract_tar",
	"git.info", "git.status", "git.list_branches", "git.worktree_create", "git.worktree_remove",
	"process.spawn", "process.stdin", "process.kill", "process.killAndWait", "process.reattach",
}

// capabilityFeatures advertises optional protocol extensions, in the reference's
// order. process.stdin.offset (the stdin-offset idempotency contract, see
// process.stdin / stdinResult) landed in 7c2f88d; 7d193f89 added two more:
// git.status.baseRepo (git.status now keys off a session worktree of baseRepo)
// and git.worktree.external_root (a worktreeRoot param places the session worktree
// OUTSIDE the repo, under that caller-chosen root, instead of inside it). external_root
// is unix-only: the reference gates that capability off on Windows and drops the feature
// from its Windows features list (externalRootCapabilityFeatures is OS-split). 4534d86
// appended server.instance_id (the capabilities reply now carries a per-boot instanceId),
// always last and on every OS. The array itself is always emitted.
var capabilityFeatures = append(append([]string{
	"process.stdin.offset",
	"git.status.baseRepo",
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
		// The reference replies {"ok":true} and then stops (measured 2026-08-27;
		// the standing battery never saw it because it shuts the daemon down on a
		// throwaway connection, and -stop reads+discards the reply). signalShutdown
		// fires here, before handleRequest writes the reply, so delivery races the
		// teardown exactly as it does on the reference — claustrum does not try to
		// out-deliver it. Under a handler panic no frame is emitted for shutdown
		// (the recover in handleRequest skips the error frame — the reference sends
		// no error frame for shutdown either).
		s.signalShutdown()
		return ptr(okResult(req.ID, shutdownResult{OK: true}))
	default:
		return ptr(unknownMethod(req))
	}
}
