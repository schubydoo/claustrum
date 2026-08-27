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
// from its Windows features list (externalRootCapabilityFeatures is OS-split). The array
// itself is always emitted.
var capabilityFeatures = append([]string{
	"process.stdin.offset",
	"git.status.baseRepo",
}, externalRootCapabilityFeatures...)

func (s *server) handleServer(c *conn, req *request) *response {
	switch req.Method {
	case "server.ping":
		return ptr(okResult(req.ID, pongResult{Pong: true}))
	case "server.capabilities":
		return ptr(okResult(req.ID, capabilitiesResult{Version: Version, Methods: capabilityMethods, Features: capabilityFeatures}))
	case methodShutdown:
		// No response is sent: the daemon stops and the connection closes.
		s.signalShutdown()
		return nil
	default:
		return ptr(unknownMethod(req))
	}
}
