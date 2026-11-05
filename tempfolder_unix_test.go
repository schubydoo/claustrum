//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// envHas reports whether env holds an entry of the name.
func envHas(env []string, name string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, name+"=") {
			return true
		}
	}
	return false
}

func TestTempFolderThatTakesNoFileIsNotHandedOn(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a 0555 folder")
	}
	sub := filepath.Join(t.TempDir(), "ro", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{sub, filepath.Dir(sub)} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Dir(sub), 0o755)
		_ = os.Chmod(sub, 0o755)
	})
	dropped := []string{"CLAUDE_CODE_TMPDIR", "TMPDIR", "TMP", "TEMP"}
	for _, name := range dropped {
		t.Setenv(name, sub)
	}
	buf := captureLogBuf(t)

	env := buildSpawnEnv(nil, false)
	at := -1
	for _, name := range dropped {
		if envHas(env, name) {
			t.Errorf("the child env holds %s, want it not handed on", name)
		}
		re := regexp.MustCompile(`(?m)^WARN  \[process\.Manager\] the inherited ` + name + `=` +
			regexp.QuoteMeta(sub) + ` is not handed on to a child: open ` +
			regexp.QuoteMeta(sub) + `/\.claude-ssh-[0-9]+: permission denied$`)
		loc := re.FindStringIndex(buf.String())
		if loc == nil {
			t.Errorf("no log line for %s, log:\n%s", name, buf.String())
			continue
		}
		// The lines come in the order of the names.
		if loc[0] < at {
			t.Errorf("the log line for %s comes before the line of the name ahead of it, log:\n%s", name, buf.String())
		}
		at = loc[0]
	}
}
