package install

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// ResolveChecked is the launcher.resolve answer for a request with a launcherCheck
// (c9125eb8, measured on Linux and macOS). p is the path member, or "" when the
// request has none. The policy value is not used: argv names p, also when the
// processWrapper of the policy names another file (measured on Linux).
//
// macOS answers its host refusal, with or without p (measured). On Linux and Windows a
// request with no p does not come here (claustrum's own, not measured: it answers as
// a request with no launcherCheck). On Windows p gets the Windows refusal
// (claustrum's own, not measured).
func ResolveChecked(p string, chk LauncherCheck) rpc.LauncherCheckResult {
	res := rpc.LauncherCheckResult{HostPolicySeen: HostPolicySeen()}
	fault, reason := LauncherCheckHostRefusal()
	if fault == "" {
		fault, reason = CheckLauncherFile(p, chk)
	}
	if fault != "" {
		res.Status, res.Reason, res.Fault = StatusUnusable, reason, fault
		return res
	}
	res.Status, res.Argv = managedStatusUsable, []string{p}
	return res
}

// HostPolicySeen reports whether a managed settings file holds a key other than
// processWrapper: the base file or a drop-in (measured on Linux and macOS). The env
// key counts as another key, as any key does. A missing file, a file that is not a
// JSON object and a file that cannot be read count as none (a file that is not JSON:
// measured on macOS. The others: claustrum's own, not measured).
func HostPolicySeen() bool {
	dir, _ := managedSettingsDir()
	if dir == "" {
		return false
	}
	files := []string{filepath.Join(dir, managedSettingsBase)}
	dropIns, _ := listManagedDropIns(filepath.Join(dir, managedSettingsDropIns))
	for _, f := range append(files, dropIns...) {
		if settingsHoldOtherKey(f) {
			return true
		}
	}
	return false
}

// settingsHoldOtherKey reports whether the settings file at path is a JSON object with
// a key other than processWrapper.
// It opens only a regular file, so a FIFO does not block it.
func settingsHoldOtherKey(path string) bool {
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	b, err := readManagedSettingsFile(path)
	if err != nil || len(b) > managedSettingsMaxBytes {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(decodeManagedSettingsText(b), &obj) != nil {
		return false
	}
	for k := range obj {
		if k != "processWrapper" {
			return true
		}
	}
	return false
}
