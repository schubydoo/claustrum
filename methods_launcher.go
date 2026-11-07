package main

import (
	"sync"

	"github.com/schubydoo/claustrum/internal/install"
	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// The managed launcher (reference build 89cb6289). A host administrator names a
// launcher in the managed settings. The launcher is a program that runs the Claude
// Code CLI on this host: the daemon puts its argv in front of the command.
// Three surfaces use it:
//
//   - launcher.resolve reads the managed settings and answers none, usable,
//     unusable or unreadable (handleLauncher below).
//   - process.spawn takes a `launcher` param and runs the child through it, or
//     refuses the spawn (supervisor.RefuseManagedLauncher in internal/supervisor/spawncheck.go, spawnVia in internal/supervisor/process.go).
//   - -install and -probe-cli run the CLI through the launcher, but only when
//     CLAUDE_SSH_MANAGED_LAUNCHER=1 (internal/install/managedlauncher_run.go).
//
// Every rule below is measured against 89cb6289 unless its comment says "claustrum's
// choice (not measured)". Every text is copied byte for byte from a measured capture.
//
// The word "launcher" alone already names the -serve parent process in server.go
// and dahandoff.go, so the identifiers here say "managed launcher".

// managedLauncherLog writes the [LauncherHandler] line of a launcher.resolve
// answer. An unusable or unreadable answer writes one line, a usable or none answer
// writes none. A repeat of the last answer writes no line, and a usable answer in
// between makes the next equal refusal log again (measured).
//
// claustrum's choices (not measured) follow. The key is the whole line, so the same
// reason from another file logs again. A none answer resets it like a usable one.
// The key lives for one daemon. The lines log at WARN.
type managedLauncherLog struct {
	mu   sync.Mutex
	last string
	// ignoredVarLogged is set once the ignored-variable line was written.
	ignoredVarLogged bool
}

// firstIgnoredVar reports true the first time only, so the ignored-variable line
// logs once per daemon.
func (l *managedLauncherLog) firstIgnoredVar() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	first := !l.ignoredVarLogged
	l.ignoredVarLogged = true
	return first
}

func (l *managedLauncherLog) note(res rpc.ManagedLauncherResult) {
	line := ""
	switch res.Status {
	case install.StatusUnusable:
		line = "[LauncherHandler] managed launcher from " + res.Source + " refused: " + res.Reason
	case install.StatusUnreadable:
		line = "[LauncherHandler] managed settings unreadable: " + res.Path + ": " + res.Reason
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if line == l.last {
		return
	}
	l.last = line
	if line != "" {
		logging.Warnf("%s", line)
	}
}

type managedLauncherParams struct {
	CliPath string `json:"cliPath"`
	// Path and LauncherCheck ask for the file check of the launcher at Path
	// (c9125eb8, install.ResolveChecked). A member of another JSON type gets Invalid
	// params (claustrum's own, not measured). An empty Path counts as absent
	// (claustrum's own, not measured).
	Path          string                 `json:"path"`
	LauncherCheck *install.LauncherCheck `json:"launcherCheck"`
}

// ExpandPaths is a no-op. claustrum's choice (not measured): cliPath is not
// `~`-expanded, so the own-path check compares the caller's text.
func (p *managedLauncherParams) ExpandPaths() {}

// launcherCheckRefusedByHost is true on macOS, where a resolve with a launcherCheck
// and no path gets the host refusal too (c9125eb8, measured on macOS). On Linux and
// Windows such a resolve answers as one with no launcherCheck (claustrum's own, not
// measured).
func launcherCheckRefusedByHost() bool {
	f, _ := install.LauncherCheckHostRefusal()
	return f == install.FaultMacOSHost
}

// handleLauncher serves the launcher namespace. Its one method is launcher.resolve.
// No params member, a non-object params or a non-string cliPath is -32602 "Invalid
// params". {}, null params, a null or empty cliPath and a wrong key are -32602
// "cliPath is required". Any other string is a cliPath, a relative one too.
// With a path and a launcherCheck the reply is the file check of c9125eb8
// (install.ResolveChecked). A path with no launcherCheck is -32602 "launcherCheck is
// required with path", after the cliPath test (measured on Linux).
func (s *server) handleLauncher(req *request) rpc.Response {
	if req.Method != "launcher.resolve" {
		return unknownMethod(req)
	}
	if bad := needParams(req); bad != nil {
		return *bad
	}
	var p managedLauncherParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.CliPath == "" {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "cliPath is required")
	}
	if p.Path != "" && p.LauncherCheck == nil {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "launcherCheck is required with path")
	}
	if p.LauncherCheck != nil && (p.Path != "" || launcherCheckRefusedByHost()) {
		res := install.ResolveChecked(p.Path, *p.LauncherCheck)
		if res.Status == install.StatusUnusable {
			// One line for each refusal, a repeat too (measured on macOS).
			logging.Warnf("[LauncherHandler] the launcher that the desktop names is refused: %s", res.Reason)
		}
		return rpc.OKResult(req.ID, res)
	}
	res, note := install.Resolve(p.CliPath)
	if note != "" && s.managedLog.firstIgnoredVar() {
		logging.Infof("%s", note)
	}
	s.managedLog.note(res)
	return rpc.OKResult(req.ID, res)
}
