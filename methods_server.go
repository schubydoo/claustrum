package main

import "runtime"

var capabilityMethods = []string{
	"server.ping", "server.version", "server.capabilities", "server.shutdown",
	"files.list", "files.validate", "files.stat", "files.read", "files.extract_tar",
	"git.info", "git.status", "git.list_branches", "git.worktree_create", "git.worktree_remove",
	"process.spawn", "process.stdin", "process.kill", "process.reattach",
}

func (s *server) handleServer(c *conn, req *request) *response {
	switch req.Method {
	case "server.ping":
		return ptr(okResult(req.ID, pongResult{Pong: true}))
	case "server.version":
		return ptr(okResult(req.ID, versionResult{Version: Version, Platform: runtime.GOOS, Arch: runtime.GOARCH}))
	case "server.capabilities":
		return ptr(okResult(req.ID, capabilitiesResult{Version: Version, Methods: capabilityMethods}))
	case "server.shutdown":
		// No response is sent: the daemon stops and the connection closes.
		s.signalShutdown()
		return nil
	default:
		return ptr(unknownMethod(req))
	}
}
