package git

import "testing"

// TestWithoutLFSLog: with a settings copy and the private LFS store the
// stderr of a failed checkout ends before the line of git-lfs that names its
// log file, and the lines after it go too (4c111e86, measured on Linux with
// git-lfs). With no private store, as on Windows, the line stays (measured on
// Windows). The test of the start of a line is claustrum's own.
func TestWithoutLFSLog(t *testing.T) {
	const log = "\nErrors logged to '/h/.checkout-1/lfs/logs/20261115T010203.log'.\nUse `git lfs logs last` to view the log.\n" +
		"error: external filter 'git-lfs filter-process' failed\nfatal: a.bin: smudge filter lfs failed\n"
	shadow := &filterRule{shadow: true, store: &storeUse{path: "/h/.checkout-1/lfs"}}
	for _, row := range []struct {
		name, stderr string
		rule         *filterRule
		want         string
	}{
		{"log line", "Error downloading object: a.bin (2351a2a): [404] Object does not exist\n" + log, shadow,
			"Error downloading object: a.bin (2351a2a): [404] Object does not exist\n\n"},
		{"log line first", log[1:], shadow, ""},
		{"words inside a line", "a.bin: Errors logged to x\nfatal: a.bin: smudge filter lfs failed\n", shadow,
			"a.bin: Errors logged to x\nfatal: a.bin: smudge filter lfs failed\n"},
		{"no log line", "fatal: a.bin: smudge filter lfs failed\n", shadow, "fatal: a.bin: smudge filter lfs failed\n"},
		{"no private store", "x\n" + log, &filterRule{shadow: true}, "x\n" + log},
		{"no settings copy", "x\n" + log, &filterRule{store: &storeUse{path: "/h/lfs"}}, "x\n" + log},
		{"no rule", "x\n" + log, nil, "x\n" + log},
	} {
		if got := withoutLFSLog(row.stderr, row.rule); got != row.want {
			t.Errorf("%s: withoutLFSLog = %q, want %q", row.name, got, row.want)
		}
	}
}
