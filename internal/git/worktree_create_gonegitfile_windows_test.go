//go:build windows

package git

import (
	"slices"
	"testing"
)

// TestCreateGoneGitFileRunsTheStartTest is the Windows row of
// TestCreateGoneGitFileStartTestStep with the text of git 2.56. The first run of
// the call about the repository exits 128 with that text and its second run exits
// 0, so the create keeps the listing (claustrum's own, not measured). Its reply
// depends on the git of the host, so the row does not check it. The row runs on
// Windows only, where git 2.56 writes that text.
// Mutation: answer "no repository" after every answer of the call in
// gitWorktreeCreateLocked.
func TestCreateGoneGitFileRunsTheStartTest(t *testing.T) {
	f, _, params := goneGitFileCreate(t, false)
	answerProbe(t, 128, "", "fatal: gitfile does not point to a valid repository: <G>/.git\n")
	answerSecondProbe(t, 0, "")
	_, calls := f.call(t, "git.worktree_create", params)
	if len(testCalls(calls)) == 0 {
		t.Error("no start test ran")
	}
	if !slices.ContainsFunc(calls, envCall.listing) {
		t.Error("no listing ran after the call about the repository")
	}
}
