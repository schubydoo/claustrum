package main

import "github.com/schubydoo/claustrum/internal/rpc"

// The names of internal/rpc under the names that the code of package main
// uses.

type (
	response       = rpc.Response
	pathExpander   = rpc.PathExpander
	jsonTrueSwitch = rpc.JSONTrueSwitch

	pongResult            = rpc.PongResult
	capabilitiesResult    = rpc.CapabilitiesResult
	shutdownResult        = rpc.ShutdownResult
	spawnResult           = rpc.SpawnResult
	gitInfoResult         = rpc.GitInfoResult
	gitStatusResult       = rpc.GitStatusResult
	branchesResult        = rpc.BranchesResult
	worktreeRemoveResult  = rpc.WorktreeRemoveResult
	worktreeResult        = rpc.WorktreeResult
	reattachResult        = rpc.ReattachResult
	stdinResult           = rpc.StdinResult
	killAndWaitResult     = rpc.KillAndWaitResult
	killResult            = rpc.KillResult
	notRepoResult         = rpc.NotRepoResult
	managedLauncherResult = rpc.ManagedLauncherResult
)

const (
	codeParse             = rpc.CodeParse
	codeInvalidReq        = rpc.CodeInvalidReq
	codeMethod            = rpc.CodeMethod
	codeInvalidParam      = rpc.CodeInvalidParam
	codeInternal          = rpc.CodeInternal
	codeUnauthorized      = rpc.CodeUnauthorized
	codeStdinOffsetGap    = rpc.CodeStdinOffsetGap
	codeStdinBackpressure = rpc.CodeStdinBackpressure
	codeLauncherUnusable  = rpc.CodeLauncherUnusable
	codeLauncherStart     = rpc.CodeLauncherStart
	codePeerRefused       = rpc.CodePeerRefused
)

func idForLog(id interface{}) string { return rpc.IDForLog(id) }

func errResult(id interface{}, code int, msg string) response { return rpc.ErrResult(id, code, msg) }

func errResultData(id interface{}, code int, msg string, data interface{}) response {
	return rpc.ErrResultData(id, code, msg, data)
}

func okResult(id interface{}, result interface{}) response { return rpc.OKResult(id, result) }

func ptr(r response) *response { return rpc.Ptr(r) }

// wire returns the five wire members of req as an rpc.Request.
func (req *request) wire() *rpc.Request {
	return &rpc.Request{JSONRPC: req.JSONRPC, ID: req.ID, Method: req.Method, Params: req.Params, Auth: req.Auth}
}

func needParams(req *request) *response { return rpc.NeedParams(req.wire()) }

func unknownMethod(req *request) response { return rpc.UnknownMethod(req.wire()) }

func decodeParams(req *request, v interface{}) error { return rpc.DecodeParams(req.wire(), v) }

func bindParams(req *request, v pathExpander) *response { return rpc.Bind(req.wire(), v) }
