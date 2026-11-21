package git

import "testing"

// A `path` that cannot be opened as a folder gets the bare reply before the
// calls in the git directory (statusPathOpenFails, 4c111e86 measured on
// Windows). The seam statusOpenTest makes the open fail on every system.
// Mutation: drop the statusOpenTest branch in gitStatus (the first row fails).
func TestStatusOpenTestAnswersNotRepo(t *testing.T) {
	f := newStatusFixture(t)
	old := statusOpenTest
	t.Cleanup(func() { statusOpenTest = old })
	for _, c := range []struct {
		fails bool
		want  string
	}{
		{true, statusNotRepo},
		{false, statusClean},
	} {
		statusOpenTest = func(string) bool { return c.fails }
		if got := statusFrame(t, f.W, f.T); got != c.want {
			t.Errorf("open fails %t: git.status = %s\nwant %s", c.fails, got, c.want)
		}
	}
}
