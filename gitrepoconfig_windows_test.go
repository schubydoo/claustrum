//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The Windows rules of the config read. 5fd08069 answered these texts on a
// Windows VM (cells C3a to C3d and C4b).

// TestWindowsIncludeTexts pins the two include texts with the paths of the
// cells. The values `~`, `/x`, `\x` and `C:x` are claustrum's own, not measured. The expected texts are the message of the reply, with the JSON
// escapes taken off.
func TestWindowsIncludeTexts(t *testing.T) {
	const config = `C:\o\w\repo\.git\config`
	notRelative := func(value string) string {
		return `"C:\\o\\w\\repo\\.git\\config" includes ` + strconv.Quote(value) +
			`, and on Windows Claude follows an include only if its path is relative and stays inside the repository; ` +
			`if that line is yours and you trust the file, put the line into ~/.gitconfig on the remote machine instead`
	}
	for _, value := range []string{"C:/o/w/absinc.cfg", "~/x", "C:/o/w/missing.cfg", "~", "/x", `\x`, "C:x"} {
		if got, want := includeValueDetail(config, value), notRelative(value); got != want {
			t.Errorf("include %s: detail = %q, want %q", value, got, want)
		}
	}
	for _, value := range []string{"inc.cfg", "../../outinc.cfg"} {
		if got := includeValueDetail(config, value); got != "" {
			t.Errorf("relative include %s: detail = %q, want none", value, got)
		}
	}
	// A value of 400 bytes is cut to its first 300 characters (cell V12b). Cells
	// P7w have the pair 300 and 301.
	long := "~root/" + strings.Repeat("x", 394)
	if got, want := includeValueDetail(config, long), notRelative(long[:300]); got != want {
		t.Errorf("include value of 400 bytes: detail = %q, want %q", got, want)
	}

	const outside = `"C:\\o\\w\\outinc.cfg" is outside "C:\\o\\w\\repo", and on Windows Claude reads a repository's ` +
		`settings only from inside it; if the line that includes it is yours and you trust the file, ` +
		`put that line into ~/.gitconfig on the remote machine instead`
	if got := includePlaceDetail(`C:\o\w\outinc.cfg`, `C:\o\w\repo\.git`); got != outside {
		t.Errorf("include outside the repository: detail = %q, want %q", got, outside)
	}
	for _, inside := range []string{`C:\o\w\repo\.git\inc.cfg`, `C:\o\w\repo\x.cfg`} {
		if got := includePlaceDetail(inside, `C:\o\w\repo\.git`); got != "" {
			t.Errorf("include %s: detail = %q, want none", inside, got)
		}
	}
}

// TestWindowsIncludeThroughTheRead runs the two include rules through the read
// of a real repository.
func TestWindowsIncludeThroughTheRead(t *testing.T) {
	repo, gitDir := configRepo(t)
	config := filepath.Join(gitDir, "config")
	outside := filepath.Join(filepath.Dir(repo), "outinc.cfg")
	if err := os.WriteFile(outside, []byte(refusedInclude), 0o644); err != nil {
		t.Fatal(err)
	}

	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = ../../outinc.cfg\n")
	want := strconv.Quote(outside) + " is outside " + strconv.Quote(repo) +
		", and on Windows Claude reads a repository's settings only from inside it; " +
		"if the line that includes it is yours and you trust the file, put that line into ~/.gitconfig on the remote machine instead"
	if got := configDetail(t, gitDir); got != want {
		t.Errorf("relative include that leaves the repository: detail = %q, want %q", got, want)
	}

	value := filepath.ToSlash(outside)
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = "+value+"\n")
	want = strconv.Quote(config) + " includes " + strconv.Quote(value) +
		", and on Windows Claude follows an include only if its path is relative and stays inside the repository; " +
		"if that line is yours and you trust the file, put the line into ~/.gitconfig on the remote machine instead"
	if got := configDetail(t, gitDir); got != want {
		t.Errorf("absolute include: detail = %q, want %q", got, want)
	}

	// A relative include inside the git directory is not refused (cell C3a).
	// claustrum follows it.
	setIncludeFile(t, gitDir, "inc.cfg", markerInclude)
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = inc.cfg\n")
	if got, want := configDetail(t, gitDir), markerDetail(filepath.Join(gitDir, "inc.cfg")); got != want {
		t.Errorf("relative include inside the git directory: detail = %q, want %q", got, want)
	}

	// The path of an included file is the folder of its file, one backslash and
	// the value as written: the slash of `sub/inc.cfg` stays (cells V12c and V12d).
	if err := os.Mkdir(filepath.Join(gitDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	setIncludeFile(t, gitDir, filepath.Join("sub", "inc.cfg"), "[include]\n\tpath = ~root/x\n")
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = sub/inc.cfg\n")
	want = strconv.Quote(gitDir+`\sub/inc.cfg`) + ` includes "~root/x"` +
		", and on Windows Claude follows an include only if its path is relative and stays inside the repository; " +
		"if that line is yours and you trust the file, put the line into ~/.gitconfig on the remote machine instead"
	if got := configDetail(t, gitDir); got != want {
		t.Errorf("include line in a file reached by sub/inc.cfg: detail = %q, want %q", got, want)
	}
}

// TestWindowsConfigLink pins the refusal of a config that is a file link (cell
// C4b). A host that refuses to make a symbolic link skips the test.
func TestWindowsConfigLink(t *testing.T) {
	repo, gitDir := configRepo(t)
	config := filepath.Join(gitDir, "config")
	realFile := filepath.Join(filepath.Dir(repo), "realconfig")
	if err := os.Rename(config, realFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realFile, config); err != nil {
		t.Skipf("this host makes no symbolic link: %v", err)
	}
	want := strconv.Quote(config) + ": is, or is reached through, a link that Claude does not follow here; " +
		"replace the link with what it points to"
	if got := configDetail(t, gitDir); got != want {
		t.Errorf("config that is a file link: detail = %q, want %q", got, want)
	}
}
