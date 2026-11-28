package main

import (
	"encoding/json"
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
	// lastChecked is the reason of the last answer to a launcherCheck, or "" after
	// a usable one.
	lastChecked string
}

// noteChecked writes the refused line of an answer to a launcherCheck. A refusal
// with the reason of the last answer writes no line. A usable answer in between
// makes it log again (4c111e86, measured on Linux). On macOS an answer with no
// launcherCheck does too (resetChecked), and a settings rewrite alone does not
// (4c111e86, measured on macOS). On Windows an answer with no launcherCheck does
// too (4c111e86, measured on Windows). On Windows 27 refusals in a row write one line
// (4c111e86, measured on Windows). claustrum's own, not measured: the key lives for
// one daemon, apart from the key of note.
func (l *managedLauncherLog) noteChecked(res rpc.LauncherCheckReply) {
	reason := ""
	if res.Status == install.StatusUnusable {
		reason = res.Reason
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if reason == l.lastChecked {
		return
	}
	l.lastChecked = reason
	if reason != "" {
		logging.Warnf("[LauncherHandler] the launcher that the desktop names is refused: %s", reason)
	}
}

// answerResetsCheck is launcherAnswerResetsCheck behind a seam, so a Linux test
// drives the macOS arm of handleLauncher. Production never reassigns it.
var answerResetsCheck = launcherAnswerResetsCheck

// resetChecked clears the key of noteChecked, so the next refusal of a launcherCheck
// logs its line.
func (l *managedLauncherLog) resetChecked() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastChecked = ""
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

func (l *managedLauncherLog) note(res rpc.ManagedLauncherReply) {
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
	// params (claustrum's own, not measured). On Windows the members inside
	// LauncherCheck can hold another type (launcherCheckParam). An empty Path counts
	// as absent (claustrum's own, not measured).
	Path          string             `json:"path"`
	LauncherCheck launcherCheckParam `json:"launcherCheck"`
}

// launcherCheckParam is the launcherCheck member of launcher.resolve. check is nil
// for an absent or null member. orManagedFolder is its orManagedFolder member, and
// pinned is true for a non-empty sha256 list or an ownerUid other than 0 (13047a51,
// handleLauncher).
type launcherCheckParam struct {
	check           *install.LauncherCheck
	orManagedFolder bool
	pinned          bool
}

// orManagedFolderPinnedText is the refusal of an orManagedFolder with sha256 or
// ownerUid (13047a51, measured on Linux, macOS and Windows).
const orManagedFolderPinnedText = "orManagedFolder can't be set with sha256 or ownerUid"

// checkTakesAnyMembers is launcherCheckAnyMembers behind a seam, so a Linux test
// drives the Windows arm of the decode. Production never reassigns it.
var checkTakesAnyMembers = launcherCheckAnyMembers

// UnmarshalJSON decodes the member. With checkTakesAnyMembers any object counts as
// a launcherCheck. On Windows 4c111e86 answers the shapes on the launcher.resolve
// page as any other, such as "path":5 (measured on Windows). Other member values are claustrum's own, not measured. Elsewhere a member of another JSON type fails the decode (claustrum's
// own, not measured). A value that is not an object and not null fails it on every
// system, and null counts as absent (4c111e86, measured on Windows for a string, "",
// a number, true, [] and an array of one object, and null as absent. Linux and macOS are claustrum's own, not measured).
// An orManagedFolder that is not a bool fails it on every system (13047a51, measured
// with a string on Linux, macOS and Windows).
func (c *launcherCheckParam) UnmarshalJSON(data []byte) error {
	var o map[string]json.RawMessage
	if err := json.Unmarshal(data, &o); err != nil || o == nil {
		return err
	}
	var m struct {
		OrManagedFolder bool `json:"orManagedFolder"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	c.orManagedFolder = m.OrManagedFolder
	if !checkTakesAnyMembers {
		if err := json.Unmarshal(data, &c.check); err != nil {
			return err
		}
		c.pinned = len(c.check.SHA256) > 0 || (c.check.OwnerUID != nil && *c.check.OwnerUID != 0)
		return nil
	}
	c.check = &install.LauncherCheck{}
	// On Windows a sha256 or ownerUid of another type does not pin (claustrum's own,
	// not measured).
	var sha []string
	var uid float64
	c.pinned = (json.Unmarshal(o["sha256"], &sha) == nil && len(sha) > 0) ||
		(json.Unmarshal(o["ownerUid"], &uid) == nil && uid != 0)
	return nil
}

// ExpandPaths is a no-op. claustrum's choice (not measured): cliPath is not
// `~`-expanded, so the own-path check compares the caller's text.
func (p *managedLauncherParams) ExpandPaths() {}

// handleLauncher serves the launcher namespace. Its one method is launcher.resolve.
// No params member, a non-object params or a non-string cliPath is -32602 "Invalid
// params". {}, null params, a null or empty cliPath and a wrong key are -32602
// "cliPath is required". Any other string is a cliPath, a relative one too.
// With a launcherCheck the reply is the file check of c9125eb8
// (install.ResolveChecked). A path with no launcherCheck is -32602 "launcherCheck is
// required with path", after the cliPath test (measured on Linux). A launcherCheck
// with no path checks "": on Linux it is unusable with not_absolute (c9125eb8,
// measured), on macOS macos_host (measured). On Windows a launcherCheck, also one
// with no path, is unusable with windows_host (4c111e86, measured on Windows for the
// shapes on the launcher.resolve page, claustrum's own for other member values).
// An orManagedFolder with a sha256 digest gets -32602 before the host refusal and the
// file check (13047a51, measured on Linux, macOS and Windows). An ownerUid of 0 does
// not count (measured on Linux and Windows). Another ownerUid and the place of the
// test after the path test are claustrum's own, not measured.
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
	if p.Path != "" && p.LauncherCheck.check == nil {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "launcherCheck is required with path")
	}
	if p.LauncherCheck.orManagedFolder && p.LauncherCheck.pinned {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, orManagedFolderPinnedText)
	}
	if p.LauncherCheck.check != nil {
		res := install.ResolveChecked(p.Path, *p.LauncherCheck.check, p.LauncherCheck.orManagedFolder)
		s.managedLog.noteChecked(res)
		return rpc.OKResult(req.ID, res)
	}
	res, note := install.Resolve(p.CliPath)
	if answerResetsCheck {
		s.managedLog.resetChecked()
	}
	if note != "" && s.managedLog.firstIgnoredVar() {
		logging.Infof("%s", note)
	}
	s.managedLog.note(res)
	return rpc.OKResult(req.ID, res)
}
