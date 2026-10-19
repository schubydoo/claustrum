package main

// Each git method decodes only the members that it reads. A member that the
// method does not read takes no part in the decode, so a wrong type there is
// ignored. 5fd08069 does the same in round task015-01: cells G1 to G3, G4b, G4d
// and G6 to G8 (Linux VM), G1 and G6 (macOS and Windows VMs). A wrong type in a
// member that the method reads stays -32602 "Invalid params" (cell G5).
//
// The methods then work on gitParams, which holds the members of all five.

// infoViewParams holds the members that git.info and git.list_branches read.
type infoViewParams struct {
	Path         string         `json:"path"`
	ShadowDirOff jsonTrueSwitch `json:"shadowDirOff,omitempty"`
}

func (q *infoViewParams) params() gitParams {
	return gitParams{Path: q.Path, ShadowDirOff: q.ShadowDirOff}
}

// statusViewParams holds the members that git.status reads.
type statusViewParams struct {
	Path         string         `json:"path"`
	BaseRepo     string         `json:"baseRepo"`
	ShadowDirOff jsonTrueSwitch `json:"shadowDirOff,omitempty"`
}

func (q *statusViewParams) params() gitParams {
	return gitParams{Path: q.Path, BaseRepo: q.BaseRepo, ShadowDirOff: q.ShadowDirOff}
}

// removeViewParams holds the members that git.worktree_remove reads.
type removeViewParams struct {
	BaseRepo     string         `json:"baseRepo"`
	BranchName   string         `json:"branchName"`
	WorktreePath string         `json:"worktreePath"`
	WorktreeRoot string         `json:"worktreeRoot,omitempty"`
	ShadowDirOff jsonTrueSwitch `json:"shadowDirOff,omitempty"`
}

func (q *removeViewParams) params() gitParams {
	return gitParams{
		BaseRepo: q.BaseRepo, BranchName: q.BranchName, WorktreePath: q.WorktreePath,
		WorktreeRoot: q.WorktreeRoot, ShadowDirOff: q.ShadowDirOff,
	}
}

// createViewParams holds the members that git.worktree_create reads.
type createViewParams struct {
	BaseRepo       string         `json:"baseRepo"`
	BranchName     string         `json:"branchName"`
	WorktreePath   string         `json:"worktreePath"`
	SourceBranch   string         `json:"sourceBranch"`
	ExistingBranch string         `json:"existingBranch,omitempty"`
	WorktreeRoot   string         `json:"worktreeRoot,omitempty"`
	TimeoutMs      int            `json:"timeoutMs,omitempty"`
	ShadowDirOff   jsonTrueSwitch `json:"shadowDirOff,omitempty"`
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
// gitParams, with each path `~`-expanded. A decode error is the reply of
// bindParams: -32602 "Invalid params". A request that came while another one
// with "shadowDirOff":true ran gets the member (request.offHeld).
func bindGitParams(req *request, view gitMethodParams) (gitParams, *response) {
	if err := decodeParams(req, view); err != nil {
		return gitParams{}, ptr(errResult(req.ID, codeInvalidParam, "Invalid params"))
	}
	p := view.params()
	if req.offHeld {
		p.ShadowDirOff = true
	}
	p.expandPaths()
	return p, nil
}
