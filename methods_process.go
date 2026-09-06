package main

import (
	"encoding/base64"
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
		return s.processStdin(req)
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
	// Absent/false leaves the reply byte-identical to {"success":true}. An older
	// daemon ignores the unknown field (bindParams tolerates it), so the param is
	// safe to send unconditionally.
	WantPid bool `json:"wantPid"`
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
	mp, err := s.procs.spawn(c, p.ID, p.Command, p.Args, p.Cwd, p.Env)
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
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
}

func (s *server) processStdin(req *request) response {
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
	mp := s.procs.get(p.ID)
	if mp == nil {
		return errResult(req.ID, codeInvalidParam, "Process not found")
	}
	applied, duplicate, gap, full, notRunning := mp.applyStdin(data, p.Offset, mp.isRunning)
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
}

func (s *server) processKill(req *request) response {
	var p killParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	s.procs.kill(p.ID, p.Signal)
	return okResult(req.ID, successResult{Success: true})
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
	found, died, alreadyExited, escalated := s.procs.killAndWait(p.ID, p.Signal, grace, escalate)
	return okResult(req.ID, killAndWaitResult{
		Found: found, Died: died, AlreadyExited: alreadyExited, Escalated: escalated,
	})
}

type reattachParams struct {
	ID      string `json:"id"`
	FromSeq uint64 `json:"fromSeq"`
	// WantPid mirrors spawnParams.WantPid — see there. When true and the process
	// is found, the reply carries pid + startTime.
	WantPid bool `json:"wantPid"`
}

func (s *server) processReattach(c *conn, req *request) response {
	var p reattachParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.ID == "" {
		return errResult(req.ID, codeInvalidParam, "Process ID is required")
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
