package main

import (
	"slices"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/files"
	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/supervisor"
)

var capabilityMethods = append(append([]string{
	"server.ping", "server.capabilities", methodShutdown,
	// c9125eb8 lists server.adoptEnv 4th, right after server.shutdown (measured on
	// Linux, macOS and Windows).
	methodAdoptEnv,
	"files.list", "files.validate", "files.stat", "files.read", "files.extract_tar",
	"git.info", "git.status", "git.list_branches", "git.worktree_create", "git.worktree_remove",
	// launcher.resolve (89cb6289) sits between git.worktree_remove and process.spawn
	// on Linux, macOS and Windows (measured). It takes the method count to 20.
	"launcher.resolve",
	"process.spawn", "process.stdin", "process.kill", "process.killAndWait", "process.reattach",
	// plugins.prune (19f30c46) is appended last.
	"plugins.prune",
	// c9125eb8 lists files.mkdir here on Linux and macOS (osCapabilityMethods).
	// 683cc6a9 lists files.realpath after plugins.prune (measured on Linux, macOS
	// and Windows). It takes the method count to 21.
}, osCapabilityMethods...), "files.realpath")

// capabilityFeatures advertises optional protocol extensions, in the reference's order.
// process.stdin.offset (the stdin-offset idempotency contract, see process.stdin / rpc.StdinResult)
// landed in 7c2f88d; 7d193f89 added two more: git.status.baseRepo (git.status now keys off a
// session worktree of baseRepo) and git.worktree.external_root (a worktreeRoot param places the
// session worktree OUTSIDE the repo, under that caller-chosen root, instead of inside it).
// external_root is unix-only: the reference gates that capability off on Windows and drops the
// feature from its Windows features list (osCapabilityFeatures is OS-split). 4534d86 inserted
// git.worktree_create.timeoutMs before external_root (a caller-supplied per-request deadline on
// the worktree add + checkout), present on every OS, and appended server.instance_id (the
// capabilities reply now carries a per-boot instanceId), always last and on every OS. 19f30c46
// inserted git.worktree_create.existingBranch after timeoutMs and before external_root
// (worktree_create can attach an already-existing branch), present on every OS like timeoutMs.
// f6010b97 inserted process.spawn.shellAgentSocket after existingBranch and before external_root
// (process.spawn hands a child the login shell's SSH_AUTH_SOCK, and takes a
// disableShellAgentSocket param), present on every OS. claustrum's Windows spawn never probes.
// 89cb6289 inserted git.worktree_remove.unpushedGuard after existingBranch and before
// shellAgentSocket. git.worktree_remove then keeps a branch that no other ref reaches, and
// answers branchKept. Linux, macOS and Windows show the feature. 89cb6289 also inserted
// launcher.managed after shellAgentSocket and before external_root (launcher.resolve and the
// process.spawn launcher param). Windows lists it too, although a Windows spawn refuses every
// launcher. 89cb6289 also inserted git.info.discovered_root after git.status.baseRepo. In that
// build git.info runs no `rev-parse --show-toplevel` on Linux and macOS (measured). Linux, macOS
// and Windows show the feature at that place. 5fd08069 inserted server.peer_check after
// launcher.managed (the peerCheck member of this result). Linux, macOS and Windows show it
// there. 5fd08069 also inserted files.extract_tar.execBit after process.stdin.offset
// (files.extract_tar keeps the execute bit of the owner as mode 0700). The same three systems
// show it at that place. 5fd08069 also lists git.shadow_dir after server.peer_check (measured on
// Linux, macOS and Windows). 52d82f0a inserted process.kill.farewell after process.stdin.offset,
// files.list.includeDotNames after files.extract_tar.execBit, and process.spawn.failure_reason
// and server.line_end after shellAgentSocket (measured on Linux, macOS and Windows). 683cc6a9
// inserted files.list.junctionsAsDirs after files.list.includeDotNames, and files.read.real
// after git.shadow_dir (measured on Linux, macOS and Windows). It also lists
// process.spawn.link_bound after external_root, on Linux and macOS only (osCapabilityFeatures).
// be1c1ca5 inserted git.hooks_null_device after git.shadow_dir (measured on Linux, macOS and
// Windows). b3081b51 inserted git.worktree_create.verified_config and
// git.worktree_create.verify_error_codes after existingBranch, launcher.non_string_refused
// after launcher.managed, git.outside_session_folders after server.peer_check and
// git.status.own_lfs_store after git.hooks_null_device (measured on Linux, macOS and
// Windows). c9125eb8 inserted process.spawn.git_read after process.spawn.failure_reason
// and launcher.supplies_cli after launcher.non_string_refused (measured on Linux, macOS
// and Windows). The array is always emitted.
var capabilityFeatures = append(append([]string{
	"process.stdin.offset",
	"process.kill.farewell",
	"files.extract_tar.execBit",
	"files.list.includeDotNames",
	"files.list.junctionsAsDirs",
	"git.status.baseRepo",
	"git.info.discovered_root",
	"git.worktree_create.timeoutMs",
	"git.worktree_create.existingBranch",
	"git.worktree_create.verified_config",
	"git.worktree_create.verify_error_codes",
	"git.worktree_remove.unpushedGuard",
	"process.spawn.shellAgentSocket",
	"process.spawn.failure_reason",
	"process.spawn.git_read",
	"server.line_end",
	"launcher.managed",
	"launcher.non_string_refused",
	"launcher.supplies_cli",
	"server.peer_check",
	"git.outside_session_folders",
	"git.shadow_dir",
	"git.hooks_null_device",
	"git.status.own_lfs_store",
	filesReadRealFeature,
}, osCapabilityFeatures...), "server.instance_id")

// filesReadRealFeature is listed only when files.ReadRealAvailable is true. The
// exact start test is claustrum's own.
const filesReadRealFeature = "files.read.real"

// jobFeature is the feature of a daemon that puts each child in a job.
const jobFeature = "process.spawn.job"

// featuresFor is the features list of this daemon. A daemon that puts each child in a
// job, which only Windows does, adds process.spawn.job after server.instance_id. With
// CLAUDE_SSH_NO_JOBS=1 the name is not in the list (`52d82f0a`, measured on Windows).
// The jobs of -keep-children (supervisor.JobsKeep) list it too (claustrum's own).
func (s *server) featuresFor() []string {
	list := capabilityFeatures
	if !files.ReadRealAvailable() {
		list = slices.DeleteFunc(slices.Clone(list), func(f string) bool { return f == filesReadRealFeature })
	}
	if s.procs == nil || s.procs.Jobs == supervisor.JobsOff {
		return list
	}
	return append(slices.Clip(list), jobFeature)
}

func (s *server) handleServer(c *conn.Conn, req *request) *rpc.Response {
	switch req.Method {
	case "server.ping":
		return rpc.Ptr(rpc.OKResult(req.ID, rpc.PongResult{Pong: true}))
	case "server.capabilities":
		return rpc.Ptr(rpc.OKResult(req.ID, rpc.CapabilitiesResult{
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
		// returns the reply at once for handleRequest to write. On Windows
		// handleRequest first waits for the drop of the connections to start
		// (awaitDropStart). See "The reply races the close" on the server.* page
		// under docs/protocol/. Under a handler panic no frame is emitted for
		// shutdown (the recover in handleRequest skips the error frame).
		//
		// The two log lines come before the teardown starts, so they precede
		// every connection close. The reference logged them in this order before
		// its connection close (measured).
		logging.Infof("[ServerHandler] server.shutdown received over RPC")
		logging.Infof("[Server] shutdown requested")
		if c != nil {
			// The cleanup count keeps this connection (dropConns).
			s.mu.Lock()
			c.SetSentStop()
			s.mu.Unlock()
		}
		s.signalShutdown()
		return rpc.Ptr(rpc.OKResult(req.ID, rpc.ShutdownResult{OK: true}))
	case methodAdoptEnv:
		return s.handleAdoptEnv(c, req)
	default:
		return rpc.Ptr(unknownMethod(req))
	}
}
