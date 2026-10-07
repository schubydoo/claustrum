//go:build windows

package main

import (
	"path/filepath"
	"slices"
	"testing"
)

// git.info through real junctions answers the frames of 89cb6289 on a Windows VM
// (cells B-01, B-03 to B-05, B-07, B-08 and B-15 to B-17k). Mutation: in
// gitDirTrustFor, call resolveWalkLinks in place of walkStart.
func TestInfoThroughJunctionWindows(t *testing.T) {
	f := newJunctionRepos(t, makeJunction)
	mixed := infoFrame(t, f.P, "rmain", "p/p", "")
	// The pin is spelled through the first junction, and the root is git's answer,
	// which names P (cell B-17k).
	t.Run("B-17k two junctions", func(t *testing.T) {
		p := filepath.Join(f.KP, "J", "sub")
		if got, want := commonDirPinEnv(p), []string{"GIT_COMMON_DIR=" + dotGitOf(f.KP)}; !slices.Equal(got, want) {
			t.Errorf("pin = %q, want %q", got, want)
		}
		wantResult(t, "info", info(t, p), mixed)
	})
	// The `..` after the junction is taken by text (cell B-17d).
	t.Run("B-17d dot-dot after the junction", func(t *testing.T) {
		p := f.J + `\..\J\sub`
		if got, want := commonDirPinEnv(p), []string{"GIT_COMMON_DIR=" + dotGitOf(f.P)}; !slices.Equal(got, want) {
			t.Errorf("pin = %q, want %q", got, want)
		}
		wantResult(t, "info", info(t, p), mixed)
	})
	checkJunctionInfoCells(t, f)
}
