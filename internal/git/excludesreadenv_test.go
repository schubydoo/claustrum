package git

import (
	"slices"
	"strings"
	"testing"
)

// The excludes read drops the five daemon names of excludesReadDropped on Windows
// and keeps the others, also GIT_CONFIG_COUNT and its items (4c111e86, measured on
// Windows). A lower-case spelling goes too, and Linux and macOS keep all five
// (claustrum's own, not measured). Mutation: excludesReadCmd without
// excludesReadEnv.
func TestExcludesReadDropsFiveNamesOnWindows(t *testing.T) {
	names := func(env []string) []string {
		var out []string
		for _, kv := range env {
			out = append(out, strings.ToUpper(envName(kv)))
		}
		return out
	}
	old := gitEnvHere
	t.Cleanup(func() { gitEnvHere = old })
	for _, windows := range []bool{true, false} {
		// A path value that is absolute on the form, so that no other rule of
		// gitChildEnv removes the name.
		abs := "/x"
		if windows {
			abs = `C:\x`
		}
		for _, kv := range []string{
			"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + abs, "GIT_CONFIG_PARAMETERS='a.b'='x'", "GIT_NO_REPLACE_OBJECTS=1",
			"GIT_REPLACE_REF_BASE=refs/r/", "git_shallow_file=" + abs,
			"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=a.k", "GIT_CONFIG_VALUE_0=v", "GIT_NAMESPACE=ns", "GIT_CEILING_DIRECTORIES=" + abs,
		} {
			name, value, _ := strings.Cut(kv, "=")
			t.Setenv(name, value)
		}
		gitEnvHere = gitEnvForm{windows: windows}
		got := names(excludesReadCmd().Env)
		for _, n := range excludesReadDropped {
			if has := slices.Contains(got, n); has == windows {
				t.Errorf("windows %t: %s present = %t, want %t", windows, n, has, !windows)
			}
		}
		for _, n := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_NAMESPACE", "GIT_DIR"} {
			if !slices.Contains(got, n) {
				t.Errorf("windows %t: %s missing from the env of the excludes read", windows, n)
			}
		}
	}
}
