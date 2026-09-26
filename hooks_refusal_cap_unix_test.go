//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The hooks refusal cuts git's raw stderr at 512 bytes before it trims white space.
// Row HK_leadlong was measured side by side against f6010b97 with a stub git on a
// Windows VM. There 40 newlines and 40 spaces come before 14 lines of 41 bytes. The
// 80 bytes of white space count toward the cap, so the text ends inside line L11. A
// trim before the cap moves the cut into line L13.
func TestHooksRefusalCapsBeforeTrim(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}
	var lines strings.Builder
	for i := 1; i <= 14; i++ {
		lines.WriteString("L" + string(rune('0'+i/10)) + string(rune('0'+i%10)) +
			" abcdefghijklmnopqrstuvwxyz0123456789\n")
	}
	payload := strings.Repeat("\n", 40) + strings.Repeat(" ", 40) + lines.String()

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "stderr.txt"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = config ]; then cat '" + filepath.Join(bin, "stderr.txt") + "' >&2; exit 128; fi\n" +
		"done\n" +
		"exec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	shapeAsGitRepo(t, dir)
	got, bad := hostileConfigRefusal(dir)
	want := "config-defined hooks could not be pinned off; git not run: " +
		"listing the configuration in force: exit status 128: " +
		strings.TrimSpace(strings.ReplaceAll(lines.String()[:512-80], "\n", " "))
	if !bad || got != want {
		t.Errorf("hostileConfigRefusal = (%q, %v)\nwant (%q, true)", got, bad, want)
	}
	if !strings.HasSuffix(got, " L11 abcdefghijklmnopqr") {
		t.Errorf("the text ends %q, want it to end inside line L11 as on f6010b97", got[len(got)-30:])
	}
}
