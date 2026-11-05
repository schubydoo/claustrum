//go:build windows

package git

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The Windows rules of the config read. 5fd08069 answered these texts on
// Windows (measured).

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
	// A value of 400 bytes is cut to its first 300 characters (measured).
	// Measurements have the pair 300 and 301.
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

// TestWindowsIncludeNotLocal pins the "is outside" text of an include below the
// repository that filepath.IsLocal does not accept. `a/inc.cfg::$DATA` and `NUL`
// are b3081b51, measured on Windows. `a:b` and `COM1` are claustrum's own, not
// measured. The other six forms pass on b3081b51 (measured on Windows).
func TestWindowsIncludeNotLocal(t *testing.T) {
	const gitDir = `C:\o\w\repo\.git`
	outside := func(cleaned string) string {
		return strconv.Quote(cleaned) + ` is outside "C:\\o\\w\\repo", and on Windows Claude reads a repository's ` +
			`settings only from inside it; if the line that includes it is yours and you trust the file, ` +
			`put that line into ~/.gitconfig on the remote machine instead`
	}
	for _, c := range []struct{ value, cleaned string }{
		{"a/inc.cfg::$DATA", gitDir + `\a\inc.cfg::$DATA`},
		{"NUL", gitDir + `\NUL`},
		{"a:b", gitDir + `\a:b`},
		{"a/COM1", gitDir + `\a\COM1`},
	} {
		if got, want := includePlaceDetail(gitDir+`\`+c.value, gitDir), outside(c.cleaned); got != want {
			t.Errorf("include %s: detail = %q, want %q", c.value, got, want)
		}
	}
	for _, value := range []string{"a/inc.cfg", "a./inc.cfg", "a /inc.cfg", "a/inc.cfg.", "a/inc.cfg ", "b/../a/inc.cfg"} {
		if got := includePlaceDetail(gitDir+`\`+value, gitDir); got != "" {
			t.Errorf("include %s: detail = %q, want none", value, got)
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

	// `NUL` and a stream name get the "is outside" text with the cleaned path, not
	// the "not a regular file" text (b3081b51, measured on Windows).
	setIncludeFile(t, gitDir, "inc.cfg", refusedInclude)
	for _, c := range []struct{ value, cleaned string }{
		{"NUL", filepath.Join(gitDir, "NUL")},
		{"inc.cfg::$DATA", filepath.Join(gitDir, "inc.cfg::$DATA")},
	} {
		setRepoConfig(t, gitDir, version0+"[include]\n\tpath = "+c.value+"\n")
		want = strconv.Quote(c.cleaned) + " is outside " + strconv.Quote(repo) +
			", and on Windows Claude reads a repository's settings only from inside it; " +
			"if the line that includes it is yours and you trust the file, put that line into ~/.gitconfig on the remote machine instead"
		if got := configDetail(t, gitDir); got != want {
			t.Errorf("include %s: detail = %q, want %q", c.value, got, want)
		}
	}

	// A relative include inside the git directory is not refused (measured).
	// claustrum follows it.
	setIncludeFile(t, gitDir, "inc.cfg", markerInclude)
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = inc.cfg\n")
	if got, want := configDetail(t, gitDir), markerDetail(filepath.Join(gitDir, "inc.cfg")); got != want {
		t.Errorf("relative include inside the git directory: detail = %q, want %q", got, want)
	}

	// The path of an included file is the folder of its file, one backslash and
	// the value as written: the slash of `sub/inc.cfg` stays (measured).
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

// TestWindowsIncludeThroughAJunction pins the link text of an include whose folder
// is a junction, with the path as written (b3081b51, measured on Windows). The
// directory symbolic link is claustrum's own, not measured. The same file by its
// real folder is followed.
func TestWindowsIncludeThroughAJunction(t *testing.T) {
	_, gitDir := configRepo(t)
	realDir := filepath.Join(gitDir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setIncludeFile(t, gitDir, filepath.Join("real", "inc.cfg"), markerInclude)
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = real/inc.cfg\n")
	if got, want := configDetail(t, gitDir), markerDetail(gitDir+`\real/inc.cfg`); got != want {
		t.Errorf("include by the real folder: detail = %q, want %q", got, want)
	}
	through := func(name string) {
		t.Helper()
		setRepoConfig(t, gitDir, version0+"[include]\n\tpath = "+name+"/inc.cfg\n")
		want := strconv.Quote(gitDir+`\`+name+`/inc.cfg`) + ": is, or is reached through, a link that Claude does not follow here; " +
			"replace the link with what it points to"
		if got := configDetail(t, gitDir); got != want {
			t.Errorf("include through %s: detail = %q, want %q", name, got, want)
		}
	}
	makeJunction(t, filepath.Join(gitDir, "jn"), realDir)
	through("jn")
	// makeDirSymlink skips the rest of the test on a host that makes no such link.
	makeDirSymlink(t, filepath.Join(gitDir, "sl"), realDir)
	through("sl")
}

// TestWindowsConfigLink pins the refusal of a config that is a file link
// (measured). A host that refuses to make a symbolic link skips the test.
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
