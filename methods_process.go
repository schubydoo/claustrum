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
	if err := s.procs.spawn(c, p.ID, p.Command, p.Args, p.Cwd, p.Env); err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	return okResult(req.ID, successResult{Success: true})
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
	if s.procs.get(p.ID) == nil {
		return errResult(req.ID, codeInvalidParam, "Process not found")
	}
	data, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil {
		return errResult(req.ID, codeInvalidParam, "Invalid params")
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
}

func (s *server) processReattach(c *conn, req *request) response {
	var p reattachParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.ID == "" {
		return errResult(req.ID, codeInvalidParam, "Process ID is required")
	}
	found, running, firstSeq, lastSeq := s.procs.reattach(c, p.ID, p.FromSeq)
	return okResult(req.ID, reattachResult{
		Found: found, Running: running, FirstSeq: firstSeq, LastSeq: lastSeq,
	})
}
