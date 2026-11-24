//go:build windows

package git

import (
	"path/filepath"
	"slices"
	"testing"
)

// git.info through real junctions answers as the repository behind them (4c111e86
// and 87375cc7, measured on Windows). Mutation: in walkStart, drop the
// junctionFreePath arm.
func TestInfoThroughJunctionWindows(t *testing.T) {
	f := newJunctionRepos(t, makeJunction)
	behind := infoFrame(t, f.R, "rmain", "r/r", "rmain")
	for name, p := range map[string]string{
		"two junctions":              filepath.Join(f.KP, "J", "sub"),
		"dot-dot after the junction": f.J + `\..\J\sub`,
	} {
		t.Run(name, func(t *testing.T) {
			if got, want := commonDirPinEnv(p), []string{"GIT_COMMON_DIR=" + dotGitOf(f.R)}; !slices.Equal(got, want) {
				t.Errorf("pin = %q, want %q", got, want)
			}
			wantResult(t, "info", info(t, p), behind)
		})
	}
	checkJunctionInfoCells(t, f)
}
