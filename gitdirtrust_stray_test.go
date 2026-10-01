package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// A commondir in a git directory that is not a linked-worktree entry (rows T00 to T11
// of 89cb6289). Each test names the mutation that turns it red.

// wantShapeRefusal is the refusal of a trusted commondir in a git directory that fails
// the git-directory test (row T10a).
func wantShapeRefusal(cd string) string {
	return wantTrustPrefix + fmt.Sprintf("%q", cd) + wantShapeTail
}

// "." and "./", with any run of trailing CR and LF, are trusted. All five
// methods are served, and git runs with the main git directory pinned (rows T01 and
// T02a to T02e). Mutation: keep refusing every commondir.
func TestStrayCommondirDotIsTrusted(t *testing.T) {
	for _, content := range []string{".", "./", ".\n", ".\r\n", "./\r\n", ".\n\n"} {
		t.Run(strings.NewReplacer("\n", `\n`, "\r", `\r`).Replace(content), func(t *testing.T) {
			r := newTrustRepo(t)
			gitDir := filepath.Join(r.T, ".git")
			writeFile(t, filepath.Join(gitDir, "commondir"), content, 0o644)
			if got, want := commonDirPinEnv(r.T), []string{"GIT_COMMON_DIR=" + gitDir}; !slices.Equal(got, want) {
				t.Errorf("pin = %q, want %q", got, want)
			}
			wantResultPrefix(t, "info(T)", info(t, r.T), `{"isRepo":true,"repo":"T"`)
			wantResultPrefix(t, "list_branches(T)", listBranches(t, r.T), `{"isRepo":true`)
			wantResultPrefix(t, "status(SW,T)", status(t, r.SW, r.T), `{"isRepo":true`)
			c, wt := create(t, r.T)
			wantResultPrefix(t, "create(T)", c, `{"success":true`)
			wantResultPrefix(t, "remove(T)", remove(t, r.T, wt, "w1"), `{"success":true`)
			if exists(wt) {
				t.Errorf("remove left %s", wt)
			}
		})
	}
}

// Any other content is refused and quoted exactly, with only the
// trailing CR and LF removed (rows T03a to T03j). A lone LF reads as "". A backslash
// is not a slash, on every OS (row T03h on a Windows VM). Mutations: trim spaces or
// tabs, clean the path, map `\` to `/` before the compare.
func TestStrayCommondirOtherContentIsRefused(t *testing.T) {
	for _, tc := range []struct{ content, quoted string }{
		{" .", " ."},
		{". ", ". "},
		{".\t", ".\t"},
		{"./x", "./x"},
		{"..", ".."},
		{"", ""},
		{"\n", ""},
		{`.\`, `.\`},
	} {
		t.Run(strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(tc.content), func(t *testing.T) {
			r := newTrustRepo(t)
			cd := filepath.Join(r.T, ".git", "commondir")
			writeFile(t, cd, tc.content, 0o644)
			wantRPCError(t, "info(T)", info(t, r.T), wantTR(cd, tc.quoted))
		})
	}
}

// The content is cut to its first 60 runes, then quoted
// with %q. An invalid UTF-8 byte stays a byte. A file of exactly 1048576 bytes is read
// and quoted (rows T04a to T04c and T06b). Mutations: cut bytes, cut at 300 runes,
// refuse 1048576 bytes as too large.
func TestStrayCommondirQuotedValue(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"100 a", strings.Repeat("a", 100), `"` + strings.Repeat("a", 60) + `"`},
		{"70 é", strings.Repeat("é", 70), `"` + strings.Repeat("é", 60) + `"`},
		{"invalid byte", "a\xffb", `"a\xffb"`},
		{"exactly 1 MiB", strings.Repeat("a", 1<<20), `"` + strings.Repeat("a", 60) + `"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTrustRepo(t)
			cd := filepath.Join(r.T, ".git", "commondir")
			writeFile(t, cd, tc.content, 0o644)
			want := wantTrustPrefix + fmt.Sprintf("%q", cd) + " reads " + tc.want + wantTRTail
			wantRPCError(t, "info(T)", info(t, r.T), want)
		})
	}
}

// A commondir that is not a small plain file is refused with its reason
// and the tail of the stray text, not the tail of an entry's text (rows T05 and T06a).
// The FIFO, the symlink and the mode-000 rows are in gitdirtrust_stray_unix_test.go.
// Mutation: reuse the tail of the entry text.
func TestStrayCommondirNotAPlainFile(t *testing.T) {
	t.Run("folder", func(t *testing.T) {
		r := newTrustRepo(t)
		cd := filepath.Join(r.T, ".git", "commondir")
		if err := os.Mkdir(cd, 0o755); err != nil {
			t.Fatal(err)
		}
		wantRPCError(t, "info(T)", info(t, r.T), wantTP(cd, "commondir is not a regular file"))
		wantCreateRefused(t, r.T, r.T, wantTP(cd, "commondir is not a regular file"))
	})
	t.Run("1048577 bytes", func(t *testing.T) {
		r := newTrustRepo(t)
		cd := filepath.Join(r.T, ".git", "commondir")
		writeFile(t, cd, strings.Repeat("a", 1<<20+1), 0o644)
		wantRPCError(t, "info(T)", info(t, r.T), wantTP(cd, "commondir is larger than 1048576 bytes"))
	})
}

// With a bad HEAD, "." gets S3, and "x" is still refused for its content: the content
// test comes first (rows T10a and T10b). Mutation: test the shape before the content.
func TestStrayCommondirBadHead(t *testing.T) {
	for _, tc := range []struct {
		content string
		want    func(cd string) string
	}{
		{".", wantShapeRefusal},
		{"x", func(cd string) string { return wantTR(cd, "x") }},
	} {
		t.Run(tc.content, func(t *testing.T) {
			r := newTrustRepo(t)
			gitDir := filepath.Join(r.T, ".git")
			writeFile(t, filepath.Join(gitDir, "HEAD"), "ref: junk\n", 0o644)
			cd := filepath.Join(gitDir, "commondir")
			writeFile(t, cd, tc.content, 0o644)
			want := tc.want(cd)
			wantRPCError(t, "info(T)", info(t, r.T), want)
			wantRPCError(t, "list_branches(T)", listBranches(t, r.T), want)
			wantCreateRefused(t, r.T, r.T, want)
		})
	}
}

// A bare repository with commondir "." is trusted and pinned to itself.
// info finds no work tree, list_branches lists main, create answers not_a_repo, and
// remove succeeds with no branchKept (row T11a). Mutation: pin nothing.
func TestStrayCommondirBareRepository(t *testing.T) {
	r := newTrustRepo(t)
	b := filepath.Join(r.base, "B.git")
	runGit(t, r.base, "clone", "-q", "--bare", "--single-branch", "-b", "main", r.T, b)
	writeFile(t, filepath.Join(b, "commondir"), ".", 0o644)
	if got, want := commonDirPinEnv(b), []string{"GIT_COMMON_DIR=" + b}; !slices.Equal(got, want) {
		t.Errorf("pin = %q, want %q", got, want)
	}
	wantResult(t, "info(B)", info(t, b), notRepoInfo)
	wantResult(t, "list_branches(B)", listBranches(t, b), `{"isRepo":true,"branches":["main"]}`)
	c, _ := create(t, b)
	wantResult(t, "create(B)", c, notRepoCreate)
	wantResult(t, "remove(B)", remove(t, b, filepath.Join(b, ".claude", "worktrees", "w1"), "w1"), `{"success":true}`)
}

// A submodule's git directory with commondir "." is trusted. The root is
// the submodule's folder, and the pin is its git directory (row T11b). Mutation:
// treat .git/modules/<name> as a linked-worktree entry.
func TestStrayCommondirSubmoduleGitDir(t *testing.T) {
	r := newTrustRepo(t)
	sub := filepath.Join(r.T, "s")
	modGit := filepath.Join(r.T, ".git", "modules", "s")
	for _, d := range []string{sub, filepath.Dir(modGit)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, r.base, "init", "-q", "-b", "main", "--separate-git-dir", modGit, sub)
	runGit(t, sub, "commit", "-q", "--allow-empty", "-m", "s0")
	writeFile(t, filepath.Join(modGit, "commondir"), ".", 0o644)
	// Both sides are canonical: on Windows git and the daemon can spell the drive
	// letter and a long name in two ways.
	if got := commonDirPinEnv(sub); len(got) != 1 ||
		canonicalPath(strings.TrimPrefix(got[0], "GIT_COMMON_DIR=")) != canonicalPath(modGit) {
		t.Errorf("pin = %q, want GIT_COMMON_DIR=%s", got, modGit)
	}
	root := sub
	if runtime.GOOS == "windows" {
		// On Windows git confirms the root and prints it with forward slashes.
		root = filepath.ToSlash(canonicalPath(sub))
	}
	wantResultPrefix(t, "info(s)", info(t, sub), `{"isRepo":true,"repo":"s","branch":"main","root":"`+jsonEscape(t, root)+`"`)
	c, wt := create(t, sub)
	wantResultPrefix(t, "create(s)", c, `{"success":true,"path":"`+jsonEscape(t, wt)+`"`)
	wantResultPrefix(t, "remove(s)", remove(t, sub, wt, "w1"), `{"success":true`)
}
