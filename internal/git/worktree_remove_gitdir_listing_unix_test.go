//go:build unix

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// failSecondListing puts a `git` first on PATH that runs the git found before it,
// except for the second `config -z --list` call. That call prints "fatal: listing
// refused" and exits 1. With versionFails, each plain `git version` after that
// failure prints "fatal: version refused" and exits 1.
func failSecondListing(t *testing.T, versionFails bool) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	count, failed := filepath.Join(bin, "count"), filepath.Join(bin, "failed")
	script := "#!/bin/sh\n" +
		"case \" $* \" in *\" config -z --list \"*)\n" +
		"  n=$(($(cat '" + count + "' 2>/dev/null || echo 0)+1)); echo $n > '" + count + "'\n" +
		"  if [ $n = 2 ]; then : > '" + failed + "'; echo 'fatal: listing refused' >&2; exit 1; fi;;\n" +
		"esac\n"
	if versionFails {
		script += "if [ \"$*\" = version ] && [ -f '" + failed + "' ]; then echo 'fatal: version refused' >&2; exit 1; fi\n"
	}
	script += "exec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A remove with worktreeRoot whose second listing fails is refused, and the worktree,
// its entry and its branch stay. That listing runs before `rev-parse
// --absolute-git-dir`, and no call runs after it but `git version`. b3081b51 answers
// so on Linux and macOS, also with a `git version` that fails (measured). The leaf
// lies in the temp folder of the test, and leafOpenedHook reports a delete that
// starts. Mutation: drop the call of gitDirCheckRefusal in externalWorkTreeRefusal.
func TestWorktreeRemoveRootSecondListingFails(t *testing.T) {
	calls := []string{r0cRead, r0cListing, "rev-parse --show-toplevel", r0cListing, "version"}
	for name, versionFails := range map[string]bool{"listing": false, "version": true} {
		t.Run(name, func(t *testing.T) {
			reason := "config-defined hooks could not be pinned off; git not run: listing the configuration " +
				"in force: exit status 1: fatal: listing refused"
			if versionFails {
				reason = "git cannot run on this host; git not run: exit status 1: fatal: version refused"
			}
			deleted := false
			leafOpenedHook = func() { deleted = true }
			t.Cleanup(func() { leafOpenedHook = nil })
			r := r0cRow{root: "<B>/X/o", wp: "<B>/X/o/cp/w1", leaf: "<B>/X/o/cp/w1", setup: withW0,
				after: func(t *testing.T, f *r0Fixture) { failSecondListing(t, versionFails) },
				calls: calls,
				want:  "failed to remove worktree: cannot determine the repository's git directory: " + reason}
			r.remove(t)
			if deleted {
				t.Error("the remove started to delete the leaf; a refusal deletes nothing")
			}
		})
	}
}
