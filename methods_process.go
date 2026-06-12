package main

import "encoding/base64"

func (s *server) handleProcess(c *conn, req *request) response {
	switch req.Method {
	case "process.spawn", "process.stdin", "process.kill", "process.reattach":
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
}

func (s *server) processStdin(req *request) response {
	var p stdinParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	// Precedence is decode → exists → running (probe-verified against the
	// reference): invalid base64 is rejected before the process is even looked
	// up, so an unknown id with a bad payload still reports the decode error.
	data, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil {
		return errResult(req.ID, codeInvalidParam, "Invalid base64 data")
	}
	mp := s.procs.get(p.ID)
	if mp == nil {
		return errResult(req.ID, codeInvalidParam, "Process not found")
	}
	if !mp.isRunning() {
		return errResult(req.ID, codeInvalidParam, "Process not running")
	}
	s.procs.writeStdin(p.ID, data)
	return okResult(req.ID, successResult{Success: true})
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

type reattachParams struct {
	ID      string `json:"id"`
	FromSeq int    `json:"fromSeq"`
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
	mp, found, running, firstSeq, lastSeq := s.procs.reattach(c, p.ID, p.FromSeq)
	res := reattachResult{
		Found: found, Running: running, FirstSeq: firstSeq, LastSeq: lastSeq,
	}
	if p.WantPid && mp != nil {
		res.Pid = mp.pid
		res.StartTime = mp.startTime
	}
	return okResult(req.ID, res)
}
