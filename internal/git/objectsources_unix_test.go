//go:build unix

package git

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A line with ".." after a symbolic link is refused for git.status, and the
// text names the link (measured on Windows). The other methods go on.
func TestAlternatesDotDotAfterLink(t *testing.T) {
	checksOn(t)
	root := realTempDir(t)
	for _, d := range []string{"sub/deep", "sub/x"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lnk := filepath.Join(root, "lnk")
	if err := os.Symlink(filepath.Join(root, "sub", "deep"), lnk); err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(root, "R", ".git")
	objects := objectsDir(t, common)
	writeFile(t, alternatesOf(objects), lnk+"/../x\n", 0o644)
	if got, want := alternatesRefusal(common, true), alternatesDotDotText(alternatesOf(objects), lnk); got != want {
		t.Errorf("git.status check:\n got %q\nwant %q", got, want)
	}
	if got := alternatesRefusal(common, false); got != "" {
		t.Errorf("check of the other methods: got %q", got)
	}
	// A link with no ".." after it passes.
	writeFile(t, alternatesOf(objects), lnk+"\n", 0o644)
	if got := alternatesRefusal(common, true); got != "" {
		t.Errorf("a link with no ..: got %q", got)
	}
	// A ".." with no link before it passes, also with a link after it (claustrum's
	// own, not measured).
	writeFile(t, alternatesOf(objects), filepath.Join(root, "sub", "deep")+"/../../lnk\n", 0o644)
	if got := alternatesRefusal(common, true); got != "" {
		t.Errorf("a .. with no link: got %q", got)
	}
}

// A chain of 31 links at baseRepo passes, and one of 32 is refused (measured on
// Windows).
func TestLinkChainCap(t *testing.T) {
	root := realTempDir(t)
	repo := filepath.Join(root, "R")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	link := func(n int) string { return filepath.Join(root, "l"+strconv.Itoa(n)) }
	prev := repo
	for n := 1; n <= 32; n++ { // the literal, not linkChainCap
		if err := os.Symlink(prev, link(n)); err != nil {
			t.Fatal(err)
		}
		prev = link(n)
	}
	if linkChainTooLong(link(31)) {
		t.Error("a chain of 31 links was refused")
	}
	if !linkChainTooLong(link(32)) {
		t.Error("a chain of 32 links passed")
	}
	// With the checks off, as on Linux and macOS, the chain is no refusal.
	old := objectSourceChecks
	objectSourceChecks = false
	if got := statusLinkChainRefusal(link(32)); got != "" {
		t.Errorf("checks off: got %q", got)
	}
	objectSourceChecks = true
	if got := statusLinkChainRefusal(link(32)); got != linkChainText(link(32)) {
		t.Errorf("checks on: got %q", got)
	}
	objectSourceChecks = old
	// A link loop is refused too.
	if err := os.Symlink(filepath.Join(root, "loopB"), filepath.Join(root, "loopA")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "loopA"), filepath.Join(root, "loopB")); err != nil {
		t.Fatal(err)
	}
	if !linkChainTooLong(filepath.Join(root, "loopA")) {
		t.Error("a loop of links passed")
	}
	// A dangling link goes on as before.
	if err := os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if linkChainTooLong(filepath.Join(root, "dangling")) {
		t.Error("a dangling link was refused")
	}
}

// git.status with a baseRepo behind 32 and 64 links, and behind a loop. b3081b51
// refuses each one and runs no git call after the excludes read (measured on
// Windows for 32 to 70 links). This test turns the Windows checks on.
func TestStatusLinkChainFrames(t *testing.T) {
	checksOn(t)
	f := newStatusFixture(t)
	call := removeCallLog(t, f.root)
	link := func(n int) string { return filepath.Join(f.root, "l"+strconv.Itoa(n)) }
	prev := f.T
	for n := 1; n <= 64; n++ {
		if err := os.Symlink(prev, link(n)); err != nil {
			t.Fatal(err)
		}
		prev = link(n)
	}
	loop := filepath.Join(f.root, "loopA")
	if err := os.Symlink(filepath.Join(f.root, "loopB"), loop); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(loop, filepath.Join(f.root, "loopB")); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{link(32), link(64), loop} {
		raw, calls := call("git.status", map[string]any{"path": base, "baseRepo": base})
		if want := errFrame(t, linkChainText(base)); raw != want {
			t.Errorf("git.status of %s = %s\nwant %s", filepath.Base(base), raw, want)
		}
		for _, c := range calls {
			if !strings.Contains(c, "|config --includes --path core.excludesFile") {
				t.Errorf("git.status of %s ran git: %q", filepath.Base(base), calls)
				break
			}
		}
	}
	// 31 links pass the test and reach git.
	if raw, _ := call("git.status", map[string]any{"path": link(31), "baseRepo": link(31)}); strings.Contains(raw, "stays on a local drive") {
		t.Errorf("git.status of 31 links = %s", raw)
	}
}
