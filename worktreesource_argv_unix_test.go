//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// logGitArgv puts a `git` first on PATH that records its argv and then runs the
// real git, and returns a reader for the calls logged so far. It never branches
// on its arguments. The reader drops the hardened light or heavy profile's -c
// options when they appear as that exact run, and every --git-dir= / --work-tree=
// option. Every other -c option is kept, in order,
// with the subcommand and its arguments, one call per slice.
func logGitArgv(t *testing.T) func() [][]string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "argv.log")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\037' \"$a\"; done >> '" + log + "'\n" +
		"printf '\\n' >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() [][]string {
		b, _ := os.ReadFile(log)
		profiles := [][]string{hardenedProfileArgs(false), hardenedProfileArgs(true)} // the -c pairs
		var calls [][]string
		for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			args := strings.Split(strings.TrimSuffix(line, "\037"), "\037")
			if len(args) >= 2 && args[0] == "-C" {
				args = args[2:]
			}
			for _, profile := range profiles {
				if len(args) >= len(profile) && strings.Join(args[:len(profile)], "\037") == strings.Join(profile, "\037") {
					args = args[len(profile):]
					break
				}
			}
			var kept []string
			for _, a := range args {
				if strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree=") {
					continue
				}
				kept = append(kept, a)
			}
			calls = append(calls, kept)
		}
		return calls
	}
}

// sourceSteps returns the logged calls from the first candidate lookup through
// the checkout, with the config enumeration that precedes each hardened call
// dropped and each id and the worktree path replaced by its label.
func sourceSteps(calls [][]string, labels map[string]string) []string {
	var steps []string
	started := false
	for _, c := range calls {
		cmd := c
		for len(cmd) >= 2 && cmd[0] == "-c" {
			cmd = cmd[2:]
		}
		if len(cmd) == 0 || cmd[0] == "config" {
			continue
		}
		if !started {
			if len(cmd) < 2 || cmd[0] != "rev-parse" || cmd[1] != "--verify" {
				continue
			}
			started = true
		}
		words := make([]string, len(c))
		for i, w := range c {
			if l, ok := labels[w]; ok {
				w = l
			}
			words[i] = w
		}
		steps = append(steps, strings.Join(words, " "))
		if cmd[0] == "read-tree" {
			break
		}
	}
	return steps
}

func wantSteps(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("git calls =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// The git calls, their order and their arguments for a diverged local branch
// that adds .claude, as measured against f6010b97. The steps use ids rather than
// ref names. The ancestry check runs as <L> <R> and the merge base as <R> <L>. The
// chosen id goes to both the add and the checkout. `rev-parse --absolute-git-dir`
// runs on the heavy profile just before the add. The -c options past the
// hardened profile are pinned too: core.commitGraph=false on each start-commit
// step and the checkout, and diff.relative=false on the diff.
func TestWorktreeSourceArgvDiverged(t *testing.T) {
	fx := newSourceFixture(t)
	mb := fx.originBranch("feat")
	runGit(t, fx.repo, "branch", "-q", "--no-track", "feat", "origin/feat")
	l := fx.localCommit("feat", "", map[string]string{".claude/settings.json": "{}\n"})
	r := fx.pushOrigin("feat", map[string]string{"marker": "remote\n"})
	calls := logGitArgv(t)
	fx.wantFromCommit("feat", r)
	wantSteps(t, sourceSteps(calls(), map[string]string{l: "<L>", r: "<R>", mb: "<MB>", fx.wtPath(): "<path>"}), []string{
		"-c core.commitGraph=false rev-parse --verify --quiet refs/heads/feat^{commit}",
		"-c core.commitGraph=false rev-parse --verify --quiet refs/remotes/origin/feat^{commit}",
		"-c core.commitGraph=false merge-base --is-ancestor <L> <R>",
		"-c core.commitGraph=false merge-base <R> <L>",
		"-c core.commitGraph=false -c diff.relative=false diff --quiet --no-ext-diff --no-textconv --submodule=short <MB> <L> -- :(top,icase).claude :(top,icase).mcp.json",
		"rev-parse --absolute-git-dir",
		"worktree add --no-track --no-checkout -b w1 <path> <R>",
		"-c core.splitIndex=false -c core.commitGraph=false read-tree -u --reset --no-recurse-submodules <R>",
	})
}

// With existingBranch naming no branch, its show-ref runs after the candidate
// steps, no HEAD fallback runs because a candidate resolved, and w1 is created
// from the chosen id. Measured against f6010b97.
func TestWorktreeSourceArgvExistingBranchMissing(t *testing.T) {
	fx := newSourceFixture(t)
	fx.originBranch("feat")
	runGit(t, fx.repo, "branch", "-q", "--no-track", "feat", "origin/feat")
	l := fx.out(fx.repo, "rev-parse", "feat")
	r := fx.pushOrigin("feat", map[string]string{"marker": "remote\n"})
	calls := logGitArgv(t)
	fx.wantCreated(fx.create(map[string]any{"sourceBranch": "feat", "existingBranch": "nope"}),
		"feat", r, "branch: Created from "+r)
	wantSteps(t, sourceSteps(calls(), map[string]string{l: "<L>", r: "<R>", fx.wtPath(): "<path>"}), []string{
		"-c core.commitGraph=false rev-parse --verify --quiet refs/heads/feat^{commit}",
		"-c core.commitGraph=false rev-parse --verify --quiet refs/remotes/origin/feat^{commit}",
		"-c core.commitGraph=false merge-base --is-ancestor <L> <R>",
		"show-ref --verify --quiet refs/heads/nope",
		"rev-parse --absolute-git-dir",
		"worktree add --no-track --no-checkout -b w1 <path> <R>",
		"-c core.splitIndex=false -c core.commitGraph=false read-tree -u --reset --no-recurse-submodules <R>",
	})
}
