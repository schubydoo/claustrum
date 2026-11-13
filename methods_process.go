package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	// (claustrum's own, not measured: before, the member was ignored). Pty is read
	// only for the refusal below, and a value other than true counts as absent.
	LinkBound *supervisor.LinkBoundParams `json:"linkBound"`
	Pty       rpc.JSONTrueSwitch          `json:"pty"`
	// CLITempRoot asks for the private temp folder step (clitemproot_linux.go). On
	// macOS and Windows it changes nothing (b3081b51, measured with true, false and
	// absent). Any other value counts as absent (claustrum's own, not measured).
	CLITempRoot rpc.JSONTrueSwitch `json:"cliTempRoot"`
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
// measured on Linux, macOS and Windows).
const gitReadCombineText = `gitRead needs the command "git" and a cwd, and cannot be combined with env, launcher, pty, linkBound, cliTempRoot or sessionFoldersOff`

// gitReadRefusal is the refusal of a "gitRead":true spawn before its start, or nil.
// The combine test comes before the shape test (c9125eb8, measured on Linux). The
// shape test comes in the start, after the cwd test (gitReadShape). The place of
// the cwd test after the combine test is claustrum's own, not measured. Measured
// members of the combine test: a non-empty env, no cwd, a
// command other than the bare name git, "sessionFoldersOff":true, a linkBound
// object, "cliTempRoot":true and a pty object with a key. An empty env object
// passes. With gitRead, "pty":true gets Invalid params (measured on Linux and
// Windows), and so does a cliTempRoot text or object (measured on Linux). A
// launcher array gets the combine text here (claustrum's own, not measured). A pty
// object with no valid key gets its own text (measured on Linux and macOS).
func gitReadRefusal(req *request, p *spawnParams) *rpc.Response {
	var raw struct {
		Pty         json.RawMessage `json:"pty"`
		CLITempRoot json.RawMessage `json:"cliTempRoot"`
	}
	_ = json.Unmarshal(req.Params, &raw)
	pty, ptyBad := gitReadMember(raw.Pty, false)
	temp, tempBad := gitReadMember(raw.CLITempRoot, true)
	if ptyBad || tempBad {
		return rpc.Ptr(rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Invalid params"))
	}
	// A pty object with no valid key gets this text with or without gitRead
	// (c9125eb8, measured on Linux and macOS). The order against the other tests is
	// claustrum's own, not measured.
	if pty && !gitReadPtyKeyOK(raw.Pty) {
		return rpc.Ptr(rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "A terminal needs a key of 32 bytes in hex"))
	}
	if p.Command != "git" || p.Cwd == "" || len(p.Env) > 0 || p.Launcher != nil || p.LinkBound != nil ||
		pty || temp || sessiondirs.OffSwitchSet(req.Params) {
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

// gitReadPtyKeyOK is true when the pty object v holds a "key" of 64 hex digits.
func gitReadPtyKeyOK(v json.RawMessage) bool {
	var o struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(v, &o) != nil || len(o.Key) != 64 {
		return false
	}
	_, err := hex.DecodeString(o.Key)
	return err == nil
}

// gitReadMember reads a pty or cliTempRoot member of a gitRead spawn. set is true
// for a member that the combine test counts. bad is true for a value that gets
// Invalid params. An absent member, null and false count as absent. For pty, true
// is bad and an object is set. For cliTempRoot, true is set and an object is bad.
// Every other value is bad. Only the values that gitReadRefusal names are
// measured: the rest is claustrum's own, not measured.
func gitReadMember(v json.RawMessage, temp bool) (set, bad bool) {
	s := string(bytes.TrimSpace(v))
	switch {
	case s == "" || s == "null" || s == "false":
		return false, false
	case s == "true":
		return temp, !temp
	case strings.HasPrefix(s, "{"):
		return !temp, temp
	}
	return false, true
}

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
	if p.ID == "" {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Process ID is required")
	}
	if bad := launcherShapeRefusal(req, &p); bad != nil {
		return *bad
	}
	if p.Command == "" && !p.LauncherSuppliesCli {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Command is required")
	}
	// 683cc6a9 refuses linkBound with a launcher by this text and with "pty":true by
	// plain Invalid params (measured on Linux). The order of the two tests and their
	// place after the id and command tests are claustrum's own, not measured.
	var linkTTL time.Duration
	if p.LinkBound != nil {
		if p.Launcher != nil {
			return rpc.ErrResultData(req.ID, rpc.CodeInvalidParam, "linkBound cannot be combined with pty or launcher", supervisor.SpawnReason{Reason: "other"})
		}
		if p.Pty {
			return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Invalid params")
		}
		linkTTL = supervisor.UnattendedTTL(p.LinkBound.UnattendedSeconds)
	}
	var mp *supervisor.Proc
	var err error
	if p.GitRead {
		if bad := gitReadRefusal(req, &p); bad != nil {
			return *bad
		}
		mp, err = s.procs.SpawnGitRead(c, p.ID, p.Args, p.Cwd, gitReadShape(p.Args), s.gitReadStep(p.Cwd, p.Args))
	} else {
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
			mp, err = s.procs.SpawnLaunched(c, p.ID, p.Command, p.Args, p.Cwd, p.Env, p.DisableShellAgentSocket, *p.Launcher, lo, folders)
		} else {
			mp, err = s.procs.SpawnWithTempRoot(c, p.ID, p.Command, p.Args, p.Cwd, p.Env, p.DisableShellAgentSocket, p.Launcher, linkTTL, supervisor.TempRootAsk{Folder: bool(p.CLITempRoot), End: bool(p.CLITempRootEnd)}, folders)
		}
	}
	if errors.Is(err, supervisor.ErrStartsStopped) {
		// No frame. The teardown closes the connection (measured).
		return rpc.Response{NoFrame: true}
	}
	if err != nil {
		return spawnErrorReply(req.ID, err, p.Cwd)
	}
	res := rpc.SpawnResult{Success: true, CLITempRoot: mp.CLITempRoot()}
	if p.WantPid {
		res.Pid = mp.Pid()
		res.StartTime = mp.StartTime()
	}
	return rpc.OKResult(req.ID, res)
}

// spawnErrorReply is the reply of a process.spawn whose start failed with err in
// cwd: the code and text of a SpawnRefusal, or -32603 and the text of err.
func spawnErrorReply(id interface{}, err error, cwd string) rpc.Response {
	reason := supervisor.SpawnFailureReason(err, cwd)
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
	return rpc.OKResult(req.ID, rpc.StdinResult{Success: true, Applied: applied, Duplicate: duplicate})
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
		return rpc.OKResult(req.ID, rpc.KillResult{Success: true, FarewellEnd: supervisor.FarewellInvalid})
	case ask != nil:
		return rpc.OKResult(req.ID, rpc.KillResult{Success: true, FarewellEnd: s.procs.KillWithFarewell(p.ID, p.Signal, *ask)})
	}
	// A signal that the system refused answers its text (52d82f0a, measured on Linux
	// and macOS). The case of a bad farewell above keeps its success (claustrum's
	// own, not measured).
	if err := s.procs.Kill(p.ID, p.Signal); err != nil {
		return rpc.ErrResult(req.ID, rpc.CodeInternal, err.Error())
	}
	return rpc.OKResult(req.ID, rpc.KillResult{Success: true})
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
	found, died, alreadyExited, escalated := s.procs.KillAndWait(p.ID, p.Signal, grace, escalate)
	end := ""
	if rule != "" {
		end = supervisor.FarewellInvalid
	}
	return rpc.OKResult(req.ID, rpc.KillAndWaitResult{
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
		return rpc.OKResult(req.ID, rpc.ReattachResult{})
	}
	mp, found, running, firstSeq, lastSeq, stdinApplied := s.procs.Reattach(c, p.ID, p.FromSeq)
	res := rpc.ReattachResult{
		Found: found, Running: running, FirstSeq: firstSeq, LastSeq: lastSeq,
		StdinApplied: stdinApplied,
	}
	if p.WantPid && mp != nil {
		res.Pid = mp.Pid()
		res.StartTime = mp.StartTime()
	}
	return rpc.OKResult(req.ID, res)
}
