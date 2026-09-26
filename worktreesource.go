package main

import "strings"

// claudeConfigPathspecs names the repository-root .claude entry and the root
// .mcp.json, matched without regard to case. A local branch whose net change
// since the merge base touches either one is not used as the start commit.
var claudeConfigPathspecs = []string{":(top,icase).claude", ":(top,icase).mcp.json"}

// pickSourceCommit chooses the commit git.worktree_create starts a new branch
// from, for a non-empty sourceBranch. It returns the full commit id, or "" when
// neither candidate resolves (the caller then falls back to HEAD).
//
// Measured side by side against f6010b97 on Linux (git 2.43.0), Windows and
// macOS:
//
//   - The candidates are refs/heads/<source> and refs/remotes/origin/<source>,
//     built by plain string concatenation and resolved with ^{commit}. So a
//     revision suffix (feat~1, feat^, feat@{1}) works, a symbolic ref is
//     followed, and an annotated tag object is peeled. An origin ref that holds a
//     missing object, a tree or garbage, or a local ref that holds a missing
//     object, counts as absent. Only the namespace origin is read. On
//     macOS a loose ref under Origin also counts.
//     Nothing is fetched.
//   - One candidate only: use it.
//   - Both, and the local one is an ancestor of the origin one (equal or behind):
//     use origin.
//   - Otherwise, with no merge base (unrelated histories, a shallow cut, a
//     missing parent): use origin.
//   - Otherwise, diff the merge base against the local commit. When the net tree
//     difference touches the root .claude or .mcp.json, or the diff fails: use
//     origin. With no such difference: use local.
//
// A merge-base or diff that fails selects origin (measured). An ancestry check
// that fails counts as "not an ancestor", and the merge-base step runs next. That
// last case is inferred: the probe produced no ancestry-check error exit.
func pickSourceCommit(repo, source string) string {
	local := resolveCommit(repo, "refs/heads/"+source)
	remote := resolveCommit(repo, "refs/remotes/origin/"+source)
	if local == "" || remote == "" {
		return local + remote // the one that resolved, or "" when neither did (one is always "")
	}
	if _, ok := hardenedGit(repo, false, "-c", "core.commitGraph=false", "merge-base", "--is-ancestor", local, remote); ok {
		return remote
	}
	// stdout only: see resolveCommit.
	base, err := hardenedGitStdout(repo, false, "-c", "core.commitGraph=false", "merge-base", remote, local)
	if err != nil {
		return remote
	}
	args := append([]string{"-c", "core.commitGraph=false", "-c", "diff.relative=false",
		"diff", "--quiet", "--no-ext-diff", "--no-textconv", "--submodule=short",
		strings.TrimSpace(base), local, "--"}, claudeConfigPathspecs...)
	if _, ok := hardenedGit(repo, false, args...); !ok {
		return remote
	}
	return local
}

// resolveCommit resolves ref^{commit} to a full commit id, or "" when it does not
// name a commit. It reads stdout only, and so does the merge-base step. git writes
// hints and warnings to stderr, for example the graft-file deprecation hints that
// appear when the environment sets GIT_GRAFT_FILE=/dev/null. With combined
// output, such a hint becomes part of the id.
//
// Each step of the start-commit choice passes -c core.commitGraph=false, and the
// diff also passes -c diff.relative=false, after the hardened profile. That is the
// argv measured against f6010b97.
func resolveCommit(repo, ref string) string {
	out, err := hardenedGitStdout(repo, false, "-c", "core.commitGraph=false", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
