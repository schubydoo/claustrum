package main

import (
	"testing"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// TestGitParamsViewsCopyEveryMember: each git struct hands every member that it
// decodes on to gitParams, and no other member.
func TestGitParamsViewsCopyEveryMember(t *testing.T) {
	raw := `{"path":"p","baseRepo":"b","branchName":"n","worktreePath":"w","sourceBranch":"s",` +
		`"existingBranch":"e","worktreeRoot":"r","timeoutMs":9,"shadowDirOff":true}`
	for _, tc := range []struct {
		view gitMethodParams
		want gitParams
	}{
		{&infoViewParams{}, gitParams{Path: "p", ShadowDirOff: true}},
		{&statusViewParams{}, gitParams{Path: "p", BaseRepo: "b", ShadowDirOff: true}},
		{&removeViewParams{}, gitParams{BaseRepo: "b", BranchName: "n", WorktreePath: "w", WorktreeRoot: "r", ShadowDirOff: true}},
		{&createViewParams{}, gitParams{BaseRepo: "b", BranchName: "n", WorktreePath: "w", SourceBranch: "s",
			ExistingBranch: "e", WorktreeRoot: "r", TimeoutMs: 9, ShadowDirOff: true}},
	} {
		got, bad := bindGitParams(&gitCall{Request: &rpc.Request{Params: []byte(raw)}}, tc.view)
		if bad != nil || got != tc.want {
			t.Errorf("%T: got %+v (reply %v), want %+v", tc.view, got, bad, tc.want)
		}
	}
}
