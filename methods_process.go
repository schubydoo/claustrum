package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

func (s *server) handleProcess(c *conn, req *request) response {
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
	// starts the process (cell P1 of round task015-01, Linux VM).
	WantPid jsonTrueSwitch `json:"wantPid"`
	// DisableShellAgentSocket turns off the login-shell SSH_AUTH_SOCK hand-off for
	// this spawn (see shellagent.go). A non-bool value fails the decode, so it
	// answers -32602 like any other mistyped param; null is accepted as false.
	DisableShellAgentSocket bool `json:"disableShellAgentSocket"`
	// Launcher is the managed launcher param (89cb6289): the child runs as
	// <launcher...> <command> <args...>. A pointer, so absent or null (no launcher)
	// differs from [] (a launcher with no argv, refused). A non-array or a
	// non-string element fails the decode (-32602 Invalid params). A null element
	// decodes to an empty arg (all measured).
	Launcher *[]string `json:"launcher"`
	// LinkBound binds the process to the connection of the spawn (683cc6a9, see
	// linkbound.go). A pointer, so absent or null is a plain spawn. A value that is
	// no object, or an unattendedSeconds that is no integer, gets Invalid params
	// (claustrum's own, not measured: before, the member was ignored). Pty is read
	// only for the refusal below, and a value other than true counts as absent.
	LinkBound *linkBoundParams `json:"linkBound"`
	Pty       jsonTrueSwitch   `json:"pty"`
}

func (s *server) processSpawn(c *conn, req *request) response {
	var p spawnParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.ID == "" {
		return errResult(req.ID, codeInvalidParam, "Process ID is required")
	}
	if p.Command == "" {
		return errResult(req.ID, codeInvalidParam, "Command is required")
	}
	// 683cc6a9 refuses linkBound with a launcher by this text and with "pty":true by
	// plain Invalid params (measured on Linux). The order of the two tests and their
	// place after the id and command tests are claustrum's own, not measured.
	var linkTTL time.Duration
	if p.LinkBound != nil {
		if p.Launcher != nil {
			return errResultData(req.ID, codeInvalidParam, "linkBound cannot be combined with pty or launcher", spawnReason{"other"})
		}
		if p.Pty {
			return errResult(req.ID, codeInvalidParam, "Invalid params")
		}
		linkTTL = unattendedTTL(p.LinkBound.UnattendedSeconds)
	}
	mp, err := s.procs.spawnVia(c, p.ID, p.Command, p.Args, p.Cwd, p.Env, p.DisableShellAgentSocket, p.Launcher, linkTTL)
	if errors.Is(err, errStartsStopped) {
		// No frame. The teardown closes the connection (round task003-01).
		return response{noFrame: true}
	}
	if err != nil {
		reason := spawnFailureReason(err, p.Cwd)
		var sr *spawnRefusal
		if errors.As(err, &sr) {
			return errResultData(req.ID, sr.code, sr.msg, reason)
		}
		return errResultData(req.ID, codeInternal, err.Error(), reason)
	}
	res := spawnResult{Success: true}
	if p.WantPid {
		res.Pid = mp.pid
		res.StartTime = mp.startTime
	}
	return okResult(req.ID, res)
}

type stdinParams struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	// Offset is the byte position this data starts at, for the stdin-offset
	// idempotency contract (advertised as "process.stdin.offset"). A pointer so an
	// absent field ("append here", the legacy behavior) is distinct from offset:0
	// (which, once anything has been applied, is a duplicate). See applyStdin.
	Offset *uint64 `json:"offset"`
	// Attach is an optional member. A pointer so null and an absent member are alike,
	// and 0 counts as present. Every process of claustrum answers a present attach as
	// not found (see processStdin), and a Linux VM measured that answer for a plain
	// process. A text or a negative number gets Invalid params from the decode
	// (measured on Linux). A fractional number gets the same here (claustrum's own,
	// not measured).
	Attach *uint64 `json:"attach"`
}

func (s *server) processStdin(c *conn, req *request) response {
	var p stdinParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	// Precedence is decode → exists → offset → running (probe-verified against
	// 4534d86, scratch/probe/stdinprec). Invalid base64 is rejected before the
	// process is even looked up. The offset idempotency verdict comes NEXT and is
	// answered even for an EXITED process: a gap returns -32003 and a wholly
	// duplicate write returns success{duplicate:true} regardless of running state.
	// Only a write that would enqueue fresh bytes requires a live process — that is
	// the one case that yields "Process not running".
	data, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil {
		return errResult(req.ID, codeInvalidParam, "Invalid base64 data")
	}
	// c8e2f083 answers "Process not found" to a present attach on a process without
	// a terminal and on an unknown id, with nothing written (measured on Linux). The
	// order against the base64 test is not measured: the attach test comes after it
	// here, and the type test of the decode comes before it. The order against the
	// offset and running tests needs no choice, because this reply writes nothing
	// and ends the request.
	// A link-bound process takes stdin from its link only. Another connection gets
	// "Process not found" (683cc6a9, measured on Linux).
	mp := s.procs.get(p.ID)
	if mp == nil || p.Attach != nil || !mp.takesFrom(c) {
		return errResult(req.ID, codeInvalidParam, "Process not found")
	}
	// isLive, not isRunning: 90fca6e6 refuses a write inside the exit drain, after
	// the child exits and before its exit frame goes out (measured on a linux VM).
	applied, duplicate, gap, full, notRunning := mp.applyStdin(data, p.Offset, mp.isLive)
	if gap {
		return errResult(req.ID, codeStdinOffsetGap, "stdin offset gap: offset ahead of applied bytes")
	}
	if notRunning {
		return errResult(req.ID, codeInvalidParam, "Process not running")
	}
	if full {
		return errResult(req.ID, codeStdinBackpressure, "stdin backpressure: queue full")
	}
	return okResult(req.ID, stdinResult{Success: true, Applied: applied, Duplicate: duplicate})
}

type killParams struct {
	ID     string `json:"id"`
	Signal string `json:"signal"`
	// Farewell and GraceMs ask for a farewell (see farewell.go). They stay raw, so
	// a value of the wrong type gets "invalid", not Invalid params (measured).
	Farewell json.RawMessage `json:"farewell"`
	GraceMs  json.RawMessage `json:"graceMs"`
}

func (s *server) processKill(req *request) response {
	var p killParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	ask, rule := readFarewell(p.Farewell, p.GraceMs)
	switch {
	case rule != "":
		// The rule test comes before the id test, so an unknown id with a bad
		// farewell gets "invalid" (claustrum's own, not measured).
		logFarewellNotSaid(p.ID, rule)
		_ = s.procs.kill(p.ID, p.Signal)
		return okResult(req.ID, killResult{Success: true, FarewellEnd: farewellInvalid})
	case ask != nil:
		return okResult(req.ID, killResult{Success: true, FarewellEnd: s.procs.killWithFarewell(p.ID, p.Signal, *ask)})
	}
	// A signal that the system refused answers its text (52d82f0a, measured on Linux
	// and macOS). The case of a bad farewell above keeps its success (claustrum's
	// own, not measured).
	if err := s.procs.kill(p.ID, p.Signal); err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	return okResult(req.ID, killResult{Success: true})
}

// killAndWaitParams is distinct from killParams: process.killAndWait accepts a
// caller-supplied grace and an escalate toggle on top of id/signal (added in
// 7c2f88d). Both are pointers so an absent field takes the reference default —
// timeoutMs → 3000ms (see clampKillWaitMs), escalate → true.
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
func (s *server) processKillAndWait(req *request) response {
	var p killAndWaitParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.ID == "" {
		return errResult(req.ID, codeInvalidParam, "Process ID is required")
	}
	ms := 0
	if p.TimeoutMs != nil {
		ms = *p.TimeoutMs
	}
	grace := time.Duration(clampKillWaitMs(ms)) * time.Millisecond
	escalate := true
	if p.Escalate != nil {
		escalate = *p.Escalate
	}
	// The farewell rules come after the id test, so the error reply stays as it was.
	ask, rule := readFarewell(p.Farewell, p.GraceMs)
	if ask != nil {
		return okResult(req.ID, s.procs.killAndWaitFarewell(p.ID, p.Signal, grace, escalate, *ask))
	}
	if rule != "" {
		// The plain kill path at once, as process.kill does (claustrum's own, not
		// measured for process.killAndWait).
		logFarewellNotSaid(p.ID, rule)
	}
	found, died, alreadyExited, escalated := s.procs.killAndWait(p.ID, p.Signal, grace, escalate)
	end := ""
	if rule != "" {
		end = farewellInvalid
	}
	return okResult(req.ID, killAndWaitResult{
		Found: found, Died: died, AlreadyExited: alreadyExited, Escalated: escalated, FarewellEnd: end,
	})
}

type reattachParams struct {
	ID      string `json:"id"`
	FromSeq uint64 `json:"fromSeq"`
	// WantPid mirrors spawnParams.WantPid — see there. When true and the process
	// is found, the reply carries pid + startTime. 5fd08069 gives the reattach
	// result with a wrong-typed wantPid (cell P2 of round task015-01, Linux VM).
	WantPid jsonTrueSwitch `json:"wantPid"`
	// Attach, Fresh and Proof are optional members. Claustrum decodes them for the
	// type test and acts on Proof only (see processReattach). A Linux VM measured the
	// proof answer of a plain process. The type test of the decode comes before the empty-id test,
	// and a negative or a fractional attach gets Invalid params there (claustrum's
	// own, not measured).
	Attach *uint64 `json:"attach"`
	Fresh  string  `json:"fresh"`
	Proof  string  `json:"proof"`
}

func (s *server) processReattach(c *conn, req *request) response {
	var p reattachParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.ID == "" {
		return errResult(req.ID, codeInvalidParam, "Process ID is required")
	}
	// A non-empty proof gets the zero result for a process without a terminal and for
	// an unknown id: no replay, and the connection that holds the process keeps it
	// (c8e2f083, measured on Linux). The order against the id test is not
	// measured: the empty id keeps its error. Claustrum has no terminal process.
	if p.Proof != "" {
		return okResult(req.ID, reattachResult{})
	}
	mp, found, running, firstSeq, lastSeq, stdinApplied := s.procs.reattach(c, p.ID, p.FromSeq)
	res := reattachResult{
		Found: found, Running: running, FirstSeq: firstSeq, LastSeq: lastSeq,
		StdinApplied: stdinApplied,
	}
	if p.WantPid && mp != nil {
		res.Pid = mp.pid
		res.StartTime = mp.startTime
	}
	return okResult(req.ID, res)
}
