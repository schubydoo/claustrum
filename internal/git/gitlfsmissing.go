package git

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/schubydoo/claustrum/internal/logging"
)

// lfsNotFoundLine is the log line of a create whose checkout needs a git-lfs that
// the git of the login-shell PATH cannot run. %s is the text of the failed
// checkout, as the reply of a failed checkout gives it (87375cc7, measured on
// Linux).
const lfsNotFoundLine = "[GitHandler] worktree_create: the checkout needs git-lfs, which lies neither beside the git from the login shell's PATH nor on the helper's own PATH; the worktree is undone and the answer is \"not a git repository\", as where no git is found. With git-lfs beside that git, or on the system's own PATH, sessions in this repository get a worktree: %s"

// lfsNotFoundForShellPathGit reports whether a checkout that failed with stderr
// failed for a git-lfs that the git of the login-shell PATH does not find. That
// git runs (ShellPathGit), git-lfs is on the login-shell PATH, and it lies neither
// beside that git nor on the PATH of the daemon. 87375cc7 then answers
// git_lfs_not_found with the text "not a git repository". The undo of a failed
// checkout first is claustrum's own. With git-lfs on no PATH the reply of a failed checkout stays
// (measured on Linux). macOS answers the failed checkout
// (measured), and Windows has no login shell here. The test of the stderr matches
// the text of dash as /bin/sh (measured on Linux). The text of bash there,
// `line 1: git-lfs: command not found`, keeps the failed checkout (claustrum's
// own, not measured).
func lfsNotFoundForShellPathGit(stderr string) bool {
	if runtime.GOOS != "linux" || !strings.Contains(stderr, "git-lfs: not found") {
		return false
	}
	git := ShellPathGit()
	if git == "" {
		return false
	}
	if _, err := exec.LookPath("git-lfs"); err == nil {
		return false
	}
	if _, err := exec.LookPath(filepath.Join(filepath.Dir(git), "git-lfs")); err == nil {
		return false
	}
	for _, dir := range filepath.SplitList(shellPathValue()) {
		if !filepath.IsAbs(dir) {
			continue
		}
		if _, err := exec.LookPath(filepath.Join(dir, "git-lfs")); err == nil {
			return true
		}
	}
	return false
}

// withShellPathGitFolder is env with the folder of git last on its PATH, when git
// is the git of the login-shell PATH (ShellPathGit). The read-tree call of a
// create gets it, so that a filter finds a git-lfs beside that git. In the state
// shadow 87375cc7 runs the git-lfs beside that git, also with another git-lfs
// first on the login-shell PATH, and the folder comes last on the PATH of the
// filter (measured on Linux). Claustrum's own, not measured: macOS, the other
// states of a create, and the PATH of the other git calls.
// gitChildEnv drops an empty PATH, so each PATH entry here holds a folder.
func withShellPathGitFolder(env []string, git string) []string {
	if git == "" || git != ShellPathGit() {
		return env
	}
	out := slices.Clone(env)
	for i, kv := range out {
		if envName(kv) == "PATH" {
			out[i] = kv + string(filepath.ListSeparator) + filepath.Dir(git)
		}
	}
	return out
}

// logLFSNotFound logs lfsNotFoundLine with the text of the failed checkout.
func logLFSNotFound(text string) {
	logging.Infof(lfsNotFoundLine, text)
}
