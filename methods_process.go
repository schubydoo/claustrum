package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/git"
	"github.com/schubydoo/claustrum/internal/install"
	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/sessiondirs"
	"github.com/schubydoo/claustrum/internal/supervisor"
)

func (s *server) handleProcess(c *conn.Conn, req *request) rpc.Response {
	switch req.Method {
	case "process.spawn", "process.stdin", "process.kill", "process.killAndWait", "process.reattach":
	default:
		return unknownMethod(req)
	}
	if bad := needParams(req); bad != nil {
		return *bad
	}
	switch req.Method {
	case "process.spawn":
		return s.processSpawn(c, req)
	case "process.stdin":
		return s.processStdin(c, req)
	case "process.kill":
		return s.processKill(req)
	case "process.killAndWait":
		return s.processKillAndWait(req)
	default: // process.reattach
		return s.processReattach(c, req)
	}
}

type spawnParams struct {
	ID      string            `json:"id"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Cwd     string            `json:"cwd"`
	Env     map[string]string `json:"env"`
	// WantPid is the CT-1 opt-in: when true the reply carries pid + startTime.
	// Absent/false leaves the reply byte-identical to {"success":true}. Any other
	// value counts as absent (claustrum's own). 5fd08069 has no such member and
	// starts the process (measured on Linux).
	WantPid rpc.JSONTrueSwitch `json:"wantPid"`
	// DisableShellAgentSocket turns off the login-shell SSH_AUTH_SOCK hand-off for
	// this spawn (see internal/shellenv/shellagent.go). A non-bool value fails the decode, so it
	// answers -32602 like any other mistyped param; null is accepted as false.
	DisableShellAgentSocket bool `json:"disableShellAgentSocket"`
	// Launcher is the managed launcher param (89cb6289): the child runs as
	// <launcher...> <command> <args...>. A pointer, so absent or null (no launcher)
	// differs from [] (a launcher with no argv, refused). A non-array or a
	// non-string element fails the decode (-32602 Invalid params). A null element
	// decodes to an empty arg (all measured).
	Launcher *[]string `json:"launcher"`
	// LinkBound binds the process to the connection of the spawn (683cc6a9, see
	// internal/supervisor/linkbound.go). A pointer, so absent or null is a plain spawn. A value that is
	// no object, or an unattendedSeconds that is no integer, gets Invalid params
	// (claustrum's own, not measured: before, the member was ignored).
	LinkBound *supervisor.LinkBoundParams `json:"linkBound"`
	// Pty is the terminal member, read only for its refusals (ptyBare, ptyMembersBad,
	// ptyKeyRefusal, keyedPtyRefusal). Absent and null give a plain spawn (measured on Linux).
	Pty json.RawMessage `json:"pty"`
	// CLITempRoot asks for the private temp folder step (clitemproot_unix.go) on Linux
	// and macOS. On Windows it changes nothing (b3081b51, measured with true, false and
	// absent). It is a bool. A text or an object fails the decode, with or without
	// gitRead (4c111e86 and c9125eb8, measured on Linux). Other types, null and the
	// other systems are claustrum's own, not measured. The decode failure comes before
	// every other test of the spawn, so it wins over the id, launcher, command and
	// gitRead refusals (claustrum's own, not measured).
	CLITempRoot bool `json:"cliTempRoot"`
	// CLITempRootEnd asks the daemon to end the child when its private temp folder is
	// lost (4c111e86, measured on Linux and macOS with true). Any other value counts
	// as absent (claustrum's own, not measured).
	CLITempRootEnd rpc.JSONTrueSwitch `json:"cliTempRootEnd"`
	// GitRead asks for the pane's git (gitReadRefusal, git.PaneGitRun). It is a
	// bool. Another JSON type fails the decode. false gives a plain spawn
	// (c9125eb8, measured on Linux). null counts as absent (claustrum's own, not
	// measured).
	GitRead bool `json:"gitRead"`
	// LauncherCheck, LauncherSuppliesCli and EnvOnlyIfSent are the launcher members
	// of c9125eb8 (launcherShapeRefusal, supervisor.LauncherOpts). A member of
	// another JSON type gets Invalid params (measured on Linux and Windows). null
	// counts as absent (claustrum's own, not measured). noLoginShell is accepted
	// and changes nothing here (measured on Linux and Windows: the spawn succeeds).
	LauncherCheck       *install.LauncherCheck `json:"launcherCheck"`
	LauncherSuppliesCli bool                   `json:"launcherSuppliesCli"`
	EnvOnlyIfSent       *[]string              `json:"envOnlyIfSent"`
	// SessionFoldersOff is read here only for its type (sessionOffTyped). The
	// switch itself is read by route (sessiondirs.OffSwitchSet).
	SessionFoldersOff json.RawMessage `json:"sessionFoldersOff"`
	// UnattendedSeconds is read for its tests (unattendedValue, unattendedRefused).
	// An accepted value above 0 arms the end of the child (supervisor.UnheldTTL).
	UnattendedSeconds json.RawMessage `json:"unattendedSeconds"`
}

// unattendedText is the refusal of an unattendedSeconds that 87375cc7 does not
// take (measured on Linux, macOS and Windows).
const unattendedText = "unattendedSeconds refused: it is negative, or came with pty, linkBound, gitRead or cliTempRootEnd, or for a Claude Code that was not started with --no-session-persistence"

// unattendedValue reads the member unattendedSeconds v. set is false when it is
// absent. ok is false for a value that is no JSON integer: 87375cc7 answers a text,
// 1.5 and null with Invalid params (measured on Linux, Windows, and on macOS for a
// text). Other forms, such as 1e3 or a number past int64, are claustrum's own.
func unattendedValue(v json.RawMessage) (n int64, set, ok bool) {
	s := bytes.TrimSpace(v)
	if len(s) == 0 {
		return 0, false, true
	}
	if string(s) == "null" || json.Unmarshal(s, &n) != nil {
		return 0, true, false
	}
	return n, true, true
}

// unattendedRefused is true for an unattendedSeconds n that 87375cc7 refuses:
// a negative value, or a positive value with a pty object, a linkBound object,
// gitRead or cliTempRootEnd, or for a spawn of a Claude Code shape
// (claudeCodeShape) without the arg --no-session-persistence. Measured on Linux:
// each of these, a pty object with a key, a command named ccd-cli or in a folder
// named ccd-cli, the bare and the two --...-format=stream-json args, and a bare git
// with a stream-json arg passes. 0 passes, also with each of these members.
// Windows measured the plain rows, a pty object with a key and the bare stream-json arg (there ccd-cli.exe
// does not count), and macOS -1, cliTempRootEnd, gitRead and a ccd-cli with a
// stream-json arg.
// A launcher spawn counts as the CLI path (measured on Linux, macOS and Windows).
func unattendedRefused(p *spawnParams, n int64) bool {
	if n < 0 {
		return true
	}
	if n == 0 {
		return false
	}
	return ptyObject(p.Pty) || p.LinkBound != nil || p.GitRead || bool(p.CLITempRootEnd) ||
		(claudeCodeShape(p.Command, p.Args, p.Launcher != nil) && !slices.Contains(p.Args, "--no-session-persistence"))
}

// sessionOffTypeRefused is true when the member sessionFoldersOff is present and
// neither a bool nor null, while sessionOffTyped holds. 4c111e86 answers
// "sessionFoldersOff":1 with Invalid params on Linux, macOS and Windows, and "x", 0
// and {} on Linux and macOS. A gitRead spawn gets it for 1 and "x" there
// (measured). On Linux and macOS true, false and null start the child
// (measured). Other values, and those three and null on Windows, are claustrum's
// own, not measured. Its place before the id, launcher and command tests is
// claustrum's own, not measured.
func sessionOffTypeRefused(v json.RawMessage) bool {
	switch string(bytes.TrimSpace(v)) {
	case "", "true", "false", "null":
		return false
	}
	return sessionOffTyped
}

// launcherShapeText is the refusal of a launcher member that lacks the member it
// needs (c9125eb8, measured on Linux, macOS and Windows).
const launcherShapeText = "invalid process.spawn: launcherCheck needs a launcher; launcherSuppliesCli needs a launcherCheck and an empty command; envOnlyIfSent needs launcherSuppliesCli"

// launcherShapeRefusal is the refusal of the launcher members, or nil. Measured: a
// launcherCheck with no launcher ({} too), a launcherSuppliesCli with a command or
// with no launcherCheck, and an envOnlyIfSent list with no launcherSuppliesCli. The
// test comes before the command test, so a supplies spawn with command "" and no
// launcherCheck gets this text, not "Command is required" (measured on Linux and
// Windows). An empty envOnlyIfSent list counts as present (claustrum's own, not
// measured). The refusal logs its failed-start line (measured on Linux).
func launcherShapeRefusal(req *request, p *spawnParams) *rpc.Response {
	if (p.LauncherCheck != nil && p.Launcher == nil) ||
		(p.LauncherSuppliesCli && (p.LauncherCheck == nil || p.Command != "")) ||
		(p.EnvOnlyIfSent != nil && !p.LauncherSuppliesCli) {
		logging.Errorf("[process.Manager] Failed to start process %s: %s", supervisor.LogID(p.ID), launcherShapeText)
		return rpc.Ptr(rpc.ErrResultData(req.ID, rpc.CodeInvalidParam, launcherShapeText, supervisor.SpawnReason{Reason: "other"}))
	}
	return nil
}

// gitReadCombineText is the refusal of a gitRead spawn with another command than
// git, with no cwd, or with a member that it cannot be combined with (c9125eb8,
// measured on Linux, macOS and Windows). The text names cliTempRootEnd too
// (4c111e86, measured on Linux). The test does not refuse cliTempRootEnd
// (claustrum's own, not measured).
const gitReadCombineText = `gitRead needs the command "git" and a cwd, and cannot be combined with env, launcher, pty, linkBound, cliTempRoot, cliTempRootEnd or sessionFoldersOff`

// gitReadRefusal is the refusal of a "gitRead":true spawn before its start, or nil.
// The combine test comes before the shape test (c9125eb8, measured on Linux). The
// shape test comes in the start, after the cwd test (gitReadShape). The place of
// the cwd test after the combine test is claustrum's own, not measured. Measured
// members of the combine test: a non-empty env, no cwd, a
// command other than the bare name git, "sessionFoldersOff":true, a linkBound
// object, "cliTempRoot":true and a pty object with a key. An empty env object
// passes. With gitRead, "pty":true gets Invalid params (measured on Linux and
// Windows), and so does a cliTempRoot text or object, from the decode (measured on
// Linux). A launcher array gets the combine text here (claustrum's own, not
// measured). A pty object with no valid key gets its own text (measured on Linux
// and macOS). The combine refusal logs its failed-start line (4c111e86, measured on
// Linux, macOS and Windows).
func gitReadRefusal(req *request, p *spawnParams) *rpc.Response {
	if bad := ptyKeyRefusal(req, p.Pty); bad != nil {
		return bad
	}
	pty := ptyObject(p.Pty)
	if p.Command != "git" || p.Cwd == "" || len(p.Env) > 0 || p.Launcher != nil || p.LinkBound != nil ||
		pty || p.CLITempRoot || sessiondirs.OffSwitchSet(req.Params) {
		logging.Errorf("[process.Manager] Failed to start process %s: %s", supervisor.LogID(p.ID), gitReadCombineText)
		return rpc.Ptr(rpc.ErrResultData(req.ID, rpc.CodeInvalidParam, gitReadCombineText, supervisor.SpawnReason{Reason: "other"}))
	}
	return nil
}

// gitReadShape is the shape refusal of a gitRead spawn with args, or nil. The
// spawn answers it with -32602 and the reason other (c9125eb8, measured on Linux,
// macOS and Windows). It comes after the cwd test (measured on Linux, macOS and
// Windows).
func gitReadShape(args []string) error {
	if msg := git.PaneShapeRefusal(args); msg != "" {
		return &supervisor.SpawnRefusal{Code: rpc.CodeInvalidParam, Msg: msg}
	}
	return nil
}

// ptyObject is true when the pty member v is a JSON object.
func ptyObject(v json.RawMessage) bool {
	return strings.HasPrefix(string(bytes.TrimSpace(v)), "{")
}

// ptyBare is true when the pty member v is present, not null and no object. Such a
// spawn gets Invalid params and starts no child. Measured on a plain spawn: true,
// false, 1 and "yes" on Linux, true and false on Windows, true on macOS (4c111e86
// and c9125eb8). The test comes right after the decode, as for a member of the
// wrong type. Its place before the id test is claustrum's own, not measured.
func ptyBare(v json.RawMessage) bool {
	s := string(bytes.TrimSpace(v))
	return s != "" && s != "null" && !ptyObject(v)
}

// ptyKeyRefusal is the refusal of a pty object with no "key" of 64 hex digits, or
// nil. Measured: {} and an object with only cols and rows, on a plain spawn on Linux
// (4c111e86 and c9125eb8) and with gitRead on Linux and macOS (c9125eb8). The text
// on a plain spawn on macOS and Windows is claustrum's own, not measured.
func ptyKeyRefusal(req *request, v json.RawMessage) *rpc.Response {
	if !ptyObject(v) || ptyKeyOK(v) {
		return nil
	}
	return rpc.Ptr(rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "A terminal needs a key of 32 bytes in hex"))
}

// ptyKeyOK is true when the pty object v holds a "key" of 64 hex digits.
func ptyKeyOK(v json.RawMessage) bool {
	var o struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(v, &o) != nil || len(o.Key) != 64 {
		return false
	}
	_, err := hex.DecodeString(o.Key)
	return err == nil
}

// keyedPtyText is the refusal of a pty object with a valid key on Windows (4c111e86
// and 87375cc7, measured on Windows).
const keyedPtyText = "this host's helper cannot open a terminal"

// ptyMembersBad is true, while keyedPtyRefused holds, for a pty object whose members
// fail the decode. Measured on Windows: a key that is a number, and cols or rows that
// are negative, a text or 10^12, and cols of 80.5. A null key passes to the key test,
// and absent or 0 cols or rows pass. claustrum's own, not measured: the 16-bit bound,
// rows of 80.5, a null cols or rows, and other key types.
func ptyMembersBad(v json.RawMessage) bool {
	if !keyedPtyRefused || !ptyObject(v) {
		return false
	}
	var o struct {
		Key  string `json:"key"`
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}
	return json.Unmarshal(v, &o) != nil
}

// keyedPtyRefusal is the refusal of a spawn with a valid pty key while keyedPtyRefused
// holds, or nil. It starts no child, writes no record and logs its failed-start line
// (measured on Windows). A gitRead spawn gets its combine text instead (measured on
// Windows).
func keyedPtyRefusal(req *request, p *spawnParams) *rpc.Response {
	if !keyedPtyRefused || p.GitRead || !ptyObject(p.Pty) || !ptyKeyOK(p.Pty) {
		return nil
	}
	logging.Errorf("[process.Manager] Failed to start process %s: %s", supervisor.LogID(p.ID), keyedPtyText)
	return rpc.Ptr(rpc.ErrResultData(req.ID, rpc.CodeInvalidParam, keyedPtyText, supervisor.SpawnReason{Reason: "other"}))
}

// The pane's git of a gitRead spawn runs the git of the login-shell PATH, as
// the git methods do, when the PATH of the daemon holds no git.
func init() { supervisor.GitReadShellPathGit = git.ShellPathGit }

// gitReadStep is the step of the start of a gitRead spawn in cwd with args
// (git.PaneGitRun).
func (s *server) gitReadStep(cwd string, args []string) supervisor.GitReadStep {
	return func(env []string) ([]string, []string, func(), error) {
		run, err := git.PaneGitRun(s.gitDeps(), cwd, args)
		if err != nil {
			return nil, nil, nil, gitReadStepError(err)
		}
		return run.Args, run.Env(env), run.Release, nil
	}
}

// gitReadStepError is the error of a failed step of a gitRead spawn. The spawn
// answers git.ErrNoSettingsCopy with -32008 (c9125eb8, measured on Linux, macOS
// and Windows), and every other error with -32603.
func gitReadStepError(err error) error {
	if errors.Is(err, git.ErrNoSettingsCopy) {
		return &supervisor.SpawnRefusal{Code: rpc.CodeNoSettingsCopy, Msg: err.Error()}
	}
	return err
}

func (s *server) processSpawn(c *conn.Conn, req *request) rpc.Response {
	var p spawnParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	// The place of the unattendedSeconds type test right after the decode is
	// claustrum's own, not measured.
	unattended, unattendedSet, unattendedOK := unattendedValue(p.UnattendedSeconds)
	if ptyBare(p.Pty) || ptyMembersBad(p.Pty) || sessionOffTypeRefused(p.SessionFoldersOff) || !unattendedOK {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Invalid params")
	}
	if p.ID == "" {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Process ID is required")
	}
	if bad := launcherShapeRefusal(req, &p); bad != nil {
		return *bad
	}
	if p.Command == "" && !p.LauncherSuppliesCli {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Command is required")
	}
	// A pty object with no valid key keeps its own text (87375cc7, measured on Linux
	// and Windows). The refusal logs its failed-start line, comes after the id and
	// command tests and before the linkBound test with a launcher (measured on
	// Linux). Its place against the launcher shape test is claustrum's own.
	if unattendedSet && unattendedRefused(&p, unattended) {
		if bad := ptyKeyRefusal(req, p.Pty); bad != nil {
			return *bad
		}
		logging.Errorf("[process.Manager] Failed to start process %s: %s", supervisor.LogID(p.ID), unattendedText)
		return rpc.ErrResultData(req.ID, rpc.CodeInvalidParam, unattendedText, supervisor.SpawnReason{Reason: "other"})
	}
	// On Windows a valid key is refused here. The refusal wins over linkBound with a
	// launcher, the launcher refusal, the cwd test, the command lookup and a live id
	// (measured on Windows). Its place after the launcher shape test, and over a given-up
	// or spent id, is claustrum's own, not measured.
	if bad := keyedPtyRefusal(req, &p); bad != nil {
		return *bad
	}
	// 683cc6a9 refuses linkBound with a launcher by this text (measured on Linux).
	// The place of the test after the id and command tests is claustrum's own, not
	// measured. "pty":true got Invalid params from the ptyBare test above.
	var linkTTL time.Duration
	if p.LinkBound != nil {
		if p.Launcher != nil {
			const linkLauncherText = "linkBound cannot be combined with pty or launcher"
			// The refusal logs its failed-start line (4c111e86, measured on Linux and
			// Windows, and 87375cc7, measured on Windows).
			logging.Errorf("[process.Manager] Failed to start process %s: %s", supervisor.LogID(p.ID), linkLauncherText)
			return rpc.ErrResultData(req.ID, rpc.CodeInvalidParam, linkLauncherText, supervisor.SpawnReason{Reason: "other"})
		}
		linkTTL = supervisor.UnattendedTTL(p.LinkBound.UnattendedSeconds)
	}
	// An accepted value above 0 arms the end of the child once no connection holds it
	// (87375cc7, measured on Linux, macOS and Windows). The test above refused it with
	// pty, linkBound, gitRead and cliTempRootEnd, and for a launcher spawn without
	// --no-session-persistence (measured on Linux, for a launcher also on macOS and Windows).
	// With that arg, also with a launcherCheck object, it is armed (claustrum's own, not measured).
	var unheld time.Duration
	if unattended > 0 {
		unheld = supervisor.UnheldTTL(unattended)
	}
	var mp *supervisor.Proc
	var err error
	if p.GitRead {
		if bad := gitReadRefusal(req, &p); bad != nil {
			return *bad
		}
		mp, err = s.procs.SpawnGitRead(c, p.ID, p.Args, p.Cwd, gitReadShape(p.Args), s.gitReadStep(p.Cwd, p.Args))
	} else {
		// The place of this test after the id, launcher, command and linkBound
		// tests is claustrum's own, not measured. On Linux and macOS a pty object with
		// a valid key gives a plain spawn with no terminal (claustrum's own: it has none).
		if bad := ptyKeyRefusal(req, p.Pty); bad != nil {
			return *bad
		}
		// The session folders of a Claude Code spawn get their records in the spawn,
		// unless the request carries the off switch (sessionfolders.go).
		var folders []string
		if !sessiondirs.OffSwitchSet(req.Params) {
			folders = sessionFoldersOf(p.Command, p.Args, p.Cwd, p.Launcher != nil)
		}
		if p.LauncherCheck != nil {
			lo := supervisor.LauncherOpts{Check: p.LauncherCheck, Supplies: p.LauncherSuppliesCli}
			if p.EnvOnlyIfSent != nil {
				lo.EnvOnlyIfSent = *p.EnvOnlyIfSent
			}
			mp, err = s.procs.SpawnLaunched(c, p.ID, p.Command, p.Args, p.Cwd, p.Env, p.DisableShellAgentSocket, *p.Launcher, lo, folders, unheld)
		} else {
			mp, err = s.procs.SpawnWithTempRoot(c, p.ID, p.Command, p.Args, p.Cwd, p.Env, p.DisableShellAgentSocket, p.Launcher, linkTTL, supervisor.TempRootAsk{Folder: p.CLITempRoot, End: bool(p.CLITempRootEnd)}, folders, unheld)
		}
	}
	if errors.Is(err, supervisor.ErrStartsStopped) {
		// No frame. The teardown closes the connection (measured).
		return rpc.Response{NoFrame: true}
	}
	if err != nil {
		return spawnErrorReply(req.ID, err, p.Cwd)
	}
	res := rpc.SpawnReply{Success: true, OutsideJob: mp.SpawnedOutside(), CLITempRoot: mp.CLITempRoot()}
	if p.WantPid {
		res.Pid = mp.Pid()
		res.StartTime = mp.StartTime()
	}
	return rpc.OKResult(req.ID, res)
}

// spawnErrorReply is the reply of a process.spawn whose start failed with err in
// cwd: the code and text of a SpawnRefusal, or -32603 and the text of err.
func spawnErrorReply(id interface{}, err error, cwd string) rpc.Response {
	reason := supervisor.ReasonForSpawnError(err, cwd)
	var sr *supervisor.SpawnRefusal
	if errors.As(err, &sr) {
		return rpc.ErrResultData(id, sr.Code, sr.Msg, reason)
	}
	return rpc.ErrResultData(id, rpc.CodeInternal, err.Error(), reason)
}

type stdinParams struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	// Offset is the byte position this data starts at, for the stdin-offset
	// idempotency contract (advertised as "process.stdin.offset"). A pointer so an
	// absent field ("append here", the legacy behavior) is distinct from offset:0
	// (which, once anything has been applied, is a duplicate). See supervisor.Proc.ApplyStdin.
	Offset *uint64 `json:"offset"`
	// Attach is an optional member. A pointer so null and an absent member are alike,
	// and 0 counts as present. Every process of claustrum answers a present attach as
	// not found (see processStdin), and Linux measured that answer for a plain
	// process. A text or a negative number gets Invalid params from the decode
	// (measured on Linux). A fractional number gets the same here (claustrum's own,
	// not measured).
	Attach *uint64 `json:"attach"`
}

func (s *server) processStdin(c *conn.Conn, req *request) rpc.Response {
	var p stdinParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	// Precedence is decode → exists → offset → running (probe-verified against
	// 4534d86). Invalid base64 is rejected before the process is even looked up.
	// The offset idempotency verdict comes NEXT and is answered even for an EXITED
	// process: a gap returns -32003 and a wholly duplicate write returns
	// success{duplicate:true} regardless of running state. Only a write that would
	// enqueue fresh bytes requires a live process — that is the one case that
	// yields "Process not running".
	data, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Invalid base64 data")
	}
	// c8e2f083 answers "Process not found" to a present attach on a process without
	// a terminal and on an unknown id, with nothing written (measured on Linux). The
	// order against the base64 test is not measured: the attach test comes after it
	// here, and the type test of the decode comes before it. The order against the
	// offset and running tests needs no choice, because this reply writes nothing
	// and ends the request.
	// A link-bound process takes stdin from its link only. Another connection gets
	// "Process not found" (683cc6a9, measured on Linux).
	mp := s.procs.Get(p.ID)
	if mp == nil || p.Attach != nil || !mp.TakesFrom(c) {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Process not found")
	}
	// IsLive, not IsRunning: 90fca6e6 refuses a write inside the exit drain, after
	// the child exits and before its exit frame goes out (measured on Linux).
	applied, duplicate, gap, full, notRunning := mp.ApplyStdin(data, p.Offset, mp.IsLive)
	if gap {
		return rpc.ErrResult(req.ID, rpc.CodeStdinOffsetGap, "stdin offset gap: offset ahead of applied bytes")
	}
	if notRunning {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Process not running")
	}
	if full {
		return rpc.ErrResult(req.ID, rpc.CodeStdinBackpressure, "stdin backpressure: queue full")
	}
	return rpc.OKResult(req.ID, rpc.StdinReply{Success: true, Applied: applied, Duplicate: duplicate})
}

type killParams struct {
	ID     string `json:"id"`
	Signal string `json:"signal"`
	// Farewell and GraceMs ask for a farewell (see internal/supervisor/farewell.go). They stay raw, so
	// a value of the wrong type gets "invalid", not Invalid params (measured).
	Farewell json.RawMessage `json:"farewell"`
	GraceMs  json.RawMessage `json:"graceMs"`
}

func (s *server) processKill(req *request) rpc.Response {
	var p killParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	// An unknown id is given up (supervisor.GiveUpUnknown, 4c111e86, measured on
	// Linux, macOS and Windows). The reply stays as before.
	s.procs.GiveUpUnknown(p.ID)
	ask, rule := supervisor.ReadFarewell(p.Farewell, p.GraceMs)
	switch {
	case rule != "":
		// The rule test comes before the id test, so an unknown id with a bad
		// farewell gets "invalid" (claustrum's own, not measured).
		supervisor.LogFarewellNotSaid(p.ID, rule)
		_ = s.procs.Kill(p.ID, p.Signal)
		return rpc.OKResult(req.ID, rpc.KillReply{Success: true, FarewellEnd: supervisor.FarewellInvalid})
	case ask != nil:
		return rpc.OKResult(req.ID, rpc.KillReply{Success: true, FarewellEnd: s.procs.KillWithFarewell(p.ID, p.Signal, *ask)})
	}
	// A signal that the system refused answers its text (52d82f0a, measured on Linux
	// and macOS). The case of a bad farewell above keeps its success (claustrum's
	// own, not measured).
	if err := s.procs.Kill(p.ID, p.Signal); err != nil {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, err.Error())
	}
	return rpc.OKResult(req.ID, rpc.KillReply{Success: true})
}

// killAndWaitParams is distinct from killParams: process.killAndWait accepts a
// caller-supplied grace and an escalate toggle on top of id/signal (added in
// 7c2f88d). Both are pointers so an absent field takes the reference default —
// timeoutMs → 3000ms (see supervisor.ClampKillWaitMs), escalate → true.
type killAndWaitParams struct {
	ID        string `json:"id"`
	Signal    string `json:"signal"`
	TimeoutMs *int   `json:"timeoutMs"`
	Escalate  *bool  `json:"escalate"`
	// Farewell and GraceMs as in killParams.
	Farewell json.RawMessage `json:"farewell"`
	GraceMs  json.RawMessage `json:"graceMs"`
}

// processKillAndWait signals a process and blocks until it is gone (or was already
// gone), unlike process.kill which is fire-and-forget. Missing id is an error;
// an unknown id is a non-error {"found":false,"died":false}. Added in 7c2f88d.
// timeoutMs sets the graceful-signal grace (non-positive/absent → the 3000ms
// default); escalate (absent → true) chooses whether to SIGKILL after the grace —
// escalate:false leaves a stubborn process running and reports died:false.
func (s *server) processKillAndWait(req *request) rpc.Response {
	var p killAndWaitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.ID == "" {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Process ID is required")
	}
	// An unknown id is given up, as on process.kill. The reply stays
	// {"found":false,"died":false} (measured on Linux and Windows).
	s.procs.GiveUpUnknown(p.ID)
	ms := 0
	if p.TimeoutMs != nil {
		ms = *p.TimeoutMs
	}
	grace := time.Duration(supervisor.ClampKillWaitMs(ms)) * time.Millisecond
	escalate := true
	if p.Escalate != nil {
		escalate = *p.Escalate
	}
	// The farewell rules come after the id test, so the error reply stays as it was.
	ask, rule := supervisor.ReadFarewell(p.Farewell, p.GraceMs)
	if ask != nil {
		return rpc.OKResult(req.ID, s.procs.KillAndWaitFarewell(p.ID, p.Signal, grace, escalate, *ask))
	}
	if rule != "" {
		// The plain kill path at once, as process.kill does (claustrum's own, not
		// measured for process.killAndWait).
		supervisor.LogFarewellNotSaid(p.ID, rule)
	}
	found, died, alreadyExited, escalated := s.procs.SignalAndAwait(p.ID, p.Signal, grace, escalate)
	end := ""
	if rule != "" {
		end = supervisor.FarewellInvalid
	}
	return rpc.OKResult(req.ID, rpc.KillAndWaitReply{
		Found: found, Died: died, AlreadyExited: alreadyExited, Escalated: escalated, FarewellEnd: end,
	})
}

type reattachParams struct {
	ID      string `json:"id"`
	FromSeq uint64 `json:"fromSeq"`
	// WantPid mirrors spawnParams.WantPid — see there. When true and the process
	// is found, the reply carries pid + startTime. 5fd08069 gives the reattach
	// result with a wrong-typed wantPid (measured on Linux).
	WantPid rpc.JSONTrueSwitch `json:"wantPid"`
	// Attach, Fresh and Proof are optional members. Claustrum decodes them for the
	// type test and acts on Proof only (see processReattach). A Linux test measured the proof answer of
	// a plain process. The type test of the decode comes before the empty-id test, and a negative
	// or a fractional attach gets Invalid params there (claustrum's own, not measured).
	Attach *uint64 `json:"attach"`
	Fresh  string  `json:"fresh"`
	Proof  string  `json:"proof"`
}

func (s *server) processReattach(c *conn.Conn, req *request) rpc.Response {
	var p reattachParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.ID == "" {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Process ID is required")
	}
	// An unknown id is given up, as on process.kill. The reply stays the result of
	// an unknown process (measured on Linux and Windows). With a proof the give-up is
	// claustrum's own, not measured.
	s.procs.GiveUpUnknown(p.ID)
	// A non-empty proof gets the zero result for a process without a terminal and for
	// an unknown id: no replay, and the connection that holds the process keeps it
	// (c8e2f083, measured on Linux). The order against the id test is not
	// measured: the empty id keeps its error. Claustrum has no terminal process.
	if p.Proof != "" {
		return rpc.OKResult(req.ID, rpc.ReattachReply{})
	}
	mp, found, running, firstSeq, lastSeq, stdinApplied := s.procs.Reattach(c, p.ID, p.FromSeq)
	res := rpc.ReattachReply{
		Found: found, Running: running, FirstSeq: firstSeq, LastSeq: lastSeq,
		StdinApplied: stdinApplied,
	}
	if p.WantPid && mp != nil {
		res.Pid = mp.Pid()
		res.StartTime = mp.StartTime()
	}
	return rpc.OKResult(req.ID, res)
}
