//go:build windows

package git

import (
	"path/filepath"
	"testing"
)

// 4c111e86 with real junctions (measured on Windows). A junction jb
// to a bare repository answers as that repository: git.info has no work tree,
// and git.list_branches lists main. A junction J to a repository T whose config
// has a bad line gets the refusal that names the config of T. Mutation: in
// walkStart, drop the junctionFreePath arm.
func TestInfoThroughJunctionToTargetWindows(t *testing.T) {
	requireGit(t)
	base := resolveTestRoot(t, t.TempDir())
	src := filepath.Join(base, "SRC")
	initTrustMain(t, src)
	bare := filepath.Join(base, "BARE")
	runGit(t, base, "clone", "-q", "--bare", src, bare)
	jb := filepath.Join(base, "jb")
	makeJunction(t, jb, bare)
	t.Run("junction to a bare repository", func(t *testing.T) {
		want := info(t, bare)
		if want.Error != nil {
			t.Fatalf("info of the bare repository = %+v", want.Error)
		}
		wantResult(t, "info", info(t, jb), string(want.Result))
		wantResult(t, "list_branches", listBranches(t, jb), `{"isRepo":true,"branches":["main"]}`)
	})
	t.Run("junction to a repository with a bad config line", func(t *testing.T) {
		target, targetGit := configRepo(t)
		setRepoConfig(t, targetGit, "[core]\n\trepositoryformatversion = 0\n"+baseRepoConfig+"[x\n\tbroken\n = = =\n")
		j := filepath.Join(base, "J")
		makeJunction(t, j, target)
		want := brokenConfigRefusal(targetGit)
		wantRPCError(t, "info", info(t, j), want)
		wantRPCError(t, "list_branches", listBranches(t, j), want)
	})
}
