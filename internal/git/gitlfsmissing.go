package git

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/schubydoo/claustrum/internal/logging"
)

// lfsNotFoundLine is the log line of a create whose checkout needs a git-lfs that
// the git of the login-shell PATH cannot run. %s is the text of the failed
// checkout, as the reply of a failed checkout gives it (87375cc7, measured on
// Linux).
const lfsNotFoundLine = "[GitHandler] worktree_create: the checkout needs git-lfs, which lies neither beside the git from the login shell's PATH nor on the helper's own PATH; the worktree is undone and the answer is \"not a git repository\", as where no git is found. With git-lfs beside that git, or on the system's own PATH, sessions in this repository get a worktree: %s"

// lfsNotFoundForLoginGit reports whether a checkout that failed with stderr
// failed for a git-lfs that the git of the login-shell PATH does not find. That
// git runs (LoginGit), git-lfs is on the login-shell PATH, and it lies neither
// beside that git nor on the PATH of the daemon. 87375cc7 then answers
// git_lfs_not_found with the text "not a git repository", after the undo of a
// failed checkout. With git-lfs on no PATH the reply of a failed checkout stays
// (measured on Linux). macOS answers the failed checkout in every build
// (measured), and Windows has no login shell here.
func lfsNotFoundForLoginGit(stderr string) bool {
	if runtime.GOOS != "linux" || !strings.Contains(stderr, "git-lfs: not found") {
		return false
	}
	git := LoginGit()
	if git == "" {
		return false
	}
	if _, err := exec.LookPath("git-lfs"); err == nil {
		return false
	}
	if _, err := exec.LookPath(filepath.Join(filepath.Dir(git), "git-lfs")); err == nil {
		return false
	}
	for _, dir := range filepath.SplitList(loginShellPATH()) {
		if !filepath.IsAbs(dir) {
			continue
		}
		if _, err := exec.LookPath(filepath.Join(dir, "git-lfs")); err == nil {
			return true
		}
	}
	return false
}

// logLFSNotFound logs lfsNotFoundLine with the text of the failed checkout.
func logLFSNotFound(text string) {
	logging.Infof(lfsNotFoundLine, text)
}
