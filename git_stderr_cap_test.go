package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// worktreeGitText makes the git text of the git.worktree_create failure frames. Each
// payload is the stderr bytes the git stub wrote, and each want is the text in the
// frame, both from the raw data measured against f6010b97 and 90fca6e6 on a macOS VM.
// The two builds agree on every row, and the checkout-failure, add-failure and
// killed-checkout frames give the same text. errExit stands for the failed git's exec
// error.
func TestWorktreeGitText(t *testing.T) {
	errExit := errors.New("exit status 128")
	errKilled := errors.New("signal: killed")
	for _, tc := range []struct {
		name, in string
		err      error
		want     string
	}{
		{"crlf", "A\r\nB\rC\tD\n", errExit, "A  B C D"},
		{"ctl", "X\x01Y\x1bZ\x7fW\n", errExit, "X Y Z W"},
		{"inval", "abc\xffdef\xc3(ghi\xe2\x82 jkl\n", errExit, "abcdef(ghi jkl"},
		// The cap cuts the 2-byte é after its first byte. That byte is dropped.
		{"a2", strings.Repeat("a", 511) + "\xc3\xa9TAIL-A2\n", errExit, strings.Repeat("a", 511)},
		// The cap cuts the 3-byte € after its first two bytes. Both are dropped.
		{"a3", strings.Repeat("a", 510) + "\xe2\x82\xacTAIL-A3\n", errExit, strings.Repeat("a", 510)},
		// The cap comes first, then the 10 invalid bytes go: 502 bytes are left.
		{"invpre", strings.Repeat("\xff", 10) + strings.Repeat("a", 600) + "\n", errExit, strings.Repeat("a", 502)},
		{"uni", "a\u0085b\u00a0c\u200bd\u2028e\u009bf\n", errExit, "a b c d e f"},
		{"ctltrim", "\x01lead\x02\n", errExit, "lead"},
		{"nul", "a\x00b\n", errExit, "a b"},
		{"ctlonly", "\x01\x02\x1b\n", errExit, "exit status 128"},
		{"ws", " \n\t\r\n  \n", errExit, "exit status 128"},
		{"empty", "", errExit, "exit status 128"},
		// The cap applies before the newline becomes a space: 300 + 1 + 211 bytes.
		{"long2", strings.Repeat("x", 300) + "\n" + strings.Repeat("y", 300) + "\n", errExit,
			strings.Repeat("x", 300) + " " + strings.Repeat("y", 211)},
		{"multi", "line1\nline2\n\nline3\n", errExit, "line1 line2  line3"},
		{"lead", "  \tlead\n", errExit, "lead"},
		// 4534d86: the two stderr lines of a refused add, on one line.
		{"two lines", "Preparing worktree (new branch 'dup')\nfatal: a branch named 'dup' already exists\n", errExit,
			"Preparing worktree (new branch 'dup') fatal: a branch named 'dup' already exists"},
		// The killed checkout (Q5): invalid bytes go anywhere, and a text with no
		// printable rune falls back to the kill's exec error.
		{"inval_mid", "abc\xffdef\n", errKilled, "abcdef"},
		{"inval_c3", "abc\xc3(def\n", errKilled, "abc(def"},
		{"inval_trunc3", "ab\xe2\x82 cd\n", errKilled, "ab cd"},
		{"inval_lead", "\xffabc\n", errKilled, "abc"},
		{"inval_end", "abc\xff", errKilled, "abc"},
		{"killed ctlonly", "\x01\x02\x1b\n", errKilled, "signal: killed"},
		// The two parts of the attach-fallback frame (Q3), each made on its own.
		{"Q3 ctl attach", "A\r\nB\tC\x01D\n", errExit, "A  B C D"},
		{"Q3 ctl fallback", "F\rG\tH\x1bI\n", errExit, "F G H I"},
		{"Q3 inval attach", "att\xffA\xc3(\n", errExit, "attA("},
		{"Q3 inval fallback", "fb\xe2\x82B\n", errExit, "fbB"},
		{"Q3 long attach", "ATT:" + strings.Repeat("a", 600) + "\n", errExit, "ATT:" + strings.Repeat("a", 508)},
		{"Q3 rune fallback", strings.Repeat("b", 510) + "\xe2\x82\xacTAIL-B\n", errExit, strings.Repeat("b", 510)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := worktreeGitText(tc.in, tc.err)
			if got != tc.want {
				t.Errorf("worktreeGitText(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// The wire bytes, too: no U+FFFD and no escaped control rune.
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			w, _ := json.Marshal(tc.want)
			if string(b) != string(w) {
				t.Errorf("encoded = %s, want %s", b, w)
			}
		})
	}
}

// 7d193f89 caps git.worktree_create's failure stderr at 512 bytes, so a long git
// error does not balloon the frame. A 600-char invalid
// branch name makes `git worktree add` fail with >512 bytes of stderr; the reference
// and claustrum both answer "git worktree add failed: " + a 512-byte head (total
// 537). Measured byte-identical against 7d193f89 on an ephemeral VM.
func TestWorktreeCreateCapsGitStderr(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	long := strings.Repeat("b/", 300) // 600 chars, an invalid branch name

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": long, "worktreePath": filepath.Join(repo, ".claude", "worktrees", "w")}))
	if !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
		t.Fatalf("worktree_create = %s, want worktree_add_failed", raw)
	}
	var resp struct {
		Result struct {
			Error string `json:"error"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	const prefix = "git worktree add failed: "
	if got, want := len(resp.Result.Error), len(prefix)+stderrHeadCap; got != want {
		t.Errorf("error length = %d, want %d (prefix + 512-byte cap); error=%q", got, want, resp.Result.Error)
	}
}
