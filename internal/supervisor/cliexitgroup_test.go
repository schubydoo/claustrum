package supervisor

import "testing"

// TestCLIExitEndsGroupShapes: the names of the rows end the group, a pty object or a
// gitRead spawn does not (claustrum's own for gitRead and the names not measured).
func TestCLIExitEndsGroupShapes(t *testing.T) {
	for _, tc := range []struct {
		command string
		ask     TempRootAsk
		gitRead bool
		want    bool
	}{
		{"/opt/x/ccd-cli", TempRootAsk{}, false, true},
		{"/opt/x/ccd-cli/2.0.0", TempRootAsk{}, false, true},
		{"ccd-cli", TempRootAsk{}, false, true},
		{"/opt/x/ccd-cli-x", TempRootAsk{}, false, false},
		{"/opt/x/claude", TempRootAsk{}, false, false},
		{"/opt/x/ccd-cli", TempRootAsk{Pty: true}, false, false},
		{"/opt/x/ccd-cli", TempRootAsk{}, true, false},
	} {
		if got := cliExitEndsGroup(tc.command, tc.ask, tc.gitRead); got != tc.want {
			t.Errorf("cliExitEndsGroup(%q, %+v, %v) = %v, want %v", tc.command, tc.ask, tc.gitRead, got, tc.want)
		}
	}
}
