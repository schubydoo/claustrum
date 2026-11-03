package git

import (
	"github.com/schubydoo/claustrum/internal/pathcanon"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// Each git method decodes only the members that it reads. A member that the
// method does not read takes no part in the decode, so a wrong type there is
// ignored. 5fd08069 does the same (measured on Linux, and in part on macOS
// and Windows). A wrong type in a member that the method reads stays -32602
// "Invalid params" (measured).
//
// The methods then work on gitParams, which holds the members of all five.

// infoViewParams holds the members that git.info and git.list_branches read.
type infoViewParams struct {
	Path         string             `json:"path"`
	ShadowDirOff rpc.JSONTrueSwitch `json:"shadowDirOff,omitempty"`
}

func (q *infoViewParams) params() gitParams {
	return gitParams{Path: q.Path, ShadowDirOff: q.ShadowDirOff}
}

// statusViewParams holds the members that git.status reads.
type statusViewParams struct {
	Path         string             `json:"path"`
	BaseRepo     string             `json:"baseRepo"`
	ShadowDirOff rpc.JSONTrueSwitch `json:"shadowDirOff,omitempty"`
}

func (q *statusViewParams) params() gitParams {
	return gitParams{Path: q.Path, BaseRepo: q.BaseRepo, ShadowDirOff: q.ShadowDirOff}
}

// removeViewParams holds the members that git.worktree_remove reads.
type removeViewParams struct {
	BaseRepo     string             `json:"baseRepo"`
	BranchName   string             `json:"branchName"`
	WorktreePath string             `json:"worktreePath"`
	WorktreeRoot string             `json:"worktreeRoot,omitempty"`
	ShadowDirOff rpc.JSONTrueSwitch `json:"shadowDirOff,omitempty"`
}

func (q *removeViewParams) params() gitParams {
	return gitParams{
		BaseRepo: q.BaseRepo, BranchName: q.BranchName, WorktreePath: q.WorktreePath,
		WorktreeRoot: q.WorktreeRoot, ShadowDirOff: q.ShadowDirOff,
	}
}

// createViewParams holds the members that git.worktree_create reads.
type createViewParams struct {
	BaseRepo       string             `json:"baseRepo"`
	BranchName     string             `json:"branchName"`
	WorktreePath   string             `json:"worktreePath"`
	SourceBranch   string             `json:"sourceBranch"`
	ExistingBranch string             `json:"existingBranch,omitempty"`
	WorktreeRoot   string             `json:"worktreeRoot,omitempty"`
	TimeoutMs      int                `json:"timeoutMs,omitempty"`
	ShadowDirOff   rpc.JSONTrueSwitch `json:"shadowDirOff,omitempty"`
}

func (q *createViewParams) params() gitParams {
	return gitParams{
		BaseRepo: q.BaseRepo, BranchName: q.BranchName, WorktreePath: q.WorktreePath,
		SourceBranch: q.SourceBranch, ExistingBranch: q.ExistingBranch,
		WorktreeRoot: q.WorktreeRoot, TimeoutMs: q.TimeoutMs, ShadowDirOff: q.ShadowDirOff,
	}
}

// gitMethodParams is one of the four structs above.
type gitMethodParams interface {
	params() gitParams
}

// bindGitParams decodes the params of req into view and returns them as
// gitParams, with each path `~`-expanded. A decode error gives
// -32602 "Invalid params". The request's own member only takes and
// releases the hold (Handle). The member of the result is the hold at this
// call, before the first git command of the method (gitCall.offLive).
func bindGitParams(req *gitCall, view gitMethodParams) (gitParams, *rpc.Response) {
	if err := rpc.DecodeParams(req.Request, view); err != nil {
		return gitParams{}, rpc.Ptr(rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Invalid params"))
	}
	p := view.params()
	req.offOwn = bool(p.ShadowDirOff)
	p.ShadowDirOff = rpc.JSONTrueSwitch(req.offLive())
	p.ExpandPaths()
	return p, nil
}

// ExpandPaths `~`-expands the path members of p, so gitParams implements
// rpc.PathExpander.
func (p *gitParams) ExpandPaths() {
	p.Path = pathcanon.ExpandTilde(p.Path)
	p.BaseRepo = pathcanon.ExpandTilde(p.BaseRepo)
	p.WorktreePath = pathcanon.ExpandTilde(p.WorktreePath)
	p.WorktreeRoot = pathcanon.ExpandTilde(p.WorktreeRoot)
	// BranchName and SourceBranch are refs, not paths — never expanded.
}
