//go:build windows

package git

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/schubydoo/claustrum/internal/pathcanon"
)

// statusProbeDir is the --work-tree value of the `rev-parse --show-toplevel`
// call for a `path` outside baseRepo. A `path` through a junction gets the
// folder as the file system reports it (pathcanon.FinalDir). Every other
// `path` gets resolvedDir, which keeps a junction.
//
// On 4c111e86 a `path` through a junction gives one `rev-parse --show-toplevel`
// with the real path, and the `status` call with the path as sent (measured on
// Windows). A `subst` drive together with a junction is not measured.
func statusProbeDir(clean string) string {
	if junctionInPath(clean) {
		return pathcanon.FinalDir(clean)
	}
	return resolvedDir(clean)
}

// statusPathOpenFails opens `path` as a folder and closes it at once. It reports
// whether the open failed. A folder with the reparse tag 0x80000008 passes
// os.Stat, and its open fails with error 1920. There 4c111e86 makes no
// `config -z --list` and no `rev-parse --show-toplevel` in the git directory:
// 15 git calls against 17 for a plain folder, and isRepo false (measured on
// Windows). Every other open failure and the place of the test right before
// statusWorkTreeProbe are claustrum's own, not measured.
func statusPathOpenFails(clean string) bool {
	f, err := os.Open(clean)
	if err != nil {
		return true
	}
	_ = f.Close()
	return false
}

// junctionInPath reports whether a folder of p, the last one included, is a
// junction or another irregular entry. Go reports a junction as an irregular
// file.
func junctionInPath(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	vol := filepath.VolumeName(abs)
	cur := vol + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(abs[len(vol):], string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeIrregular != 0 {
			return true
		}
	}
	return false
}
