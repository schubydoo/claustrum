//go:build windows

package supervisor

import (
	"testing"
	"time"
)

// TestGitReadLimitEndsTheGitOnWindows: at the limit the pane's git ends with exit
// code 1, and its exit frame has the killedBy "limit" and no signal member
// (c9125eb8, measured on Windows). The limit is shortened.
func TestGitReadLimitEndsTheGitOnWindows(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setGitReadLimitForTest(t, 2*time.Second, 300*time.Millisecond)
	c, lines := rawLineConn(t)
	spawnHelperGitRead(t, m, c, "sleep", "60")
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 15*time.Second))
	const want = `{"type":"stream","processId":"gr","stream":"exit","seq":1,"exitCode":1,"killedBy":"limit","end":{"at":<n>,"len":89}}`
	if exit != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
	wantLinesInOrder(t, logs.String(),
		"[process.Manager] the pane's git has run for 2s; ending it",
		"[process.Manager] Process gr exited with code 1, asked to end by this daemon (limit)")
}
