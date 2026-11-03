package main

import "github.com/schubydoo/claustrum/internal/rpc"

// The names of internal/rpc under the names that the code of package main
// uses.

type (
	response     = rpc.Response
	pathExpander = rpc.PathExpander

	pongResult         = rpc.PongResult
	capabilitiesResult = rpc.CapabilitiesResult
	spawnResult        = rpc.SpawnResult
	reattachResult     = rpc.ReattachResult
	stdinResult        = rpc.StdinResult
	killAndWaitResult  = rpc.KillAndWaitResult
)

const (
	codeInvalidParam      = rpc.CodeInvalidParam
	codeStdinOffsetGap    = rpc.CodeStdinOffsetGap
	codeStdinBackpressure = rpc.CodeStdinBackpressure
)

func idForLog(id interface{}) string { return rpc.IDForLog(id) }
