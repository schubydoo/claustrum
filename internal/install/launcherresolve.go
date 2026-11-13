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
// macOS answers its host refusal with no p (measured). With p it does the same
// (claustrum's own, not measured). On Linux a request with no p comes here and
// gets not_absolute (measured on Linux). On Windows a request with no p does not come
// here: it answers as a request with no launcherCheck. On Windows p gets the Windows
// refusal. Both Windows rules are claustrum's own, not measured.
func ResolveChecked(p string, chk LauncherCheck) rpc.LauncherCheckResult {
	res := rpc.LauncherCheckResult{HostPolicySeen: policyHoldsOtherKey()}
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

// policyHoldsOtherKey reports whether a managed settings file holds a key other than
// processWrapper: the base file or a drop-in (measured on Linux and macOS). The env
// key counts as another key, as any key does. A missing file counts as none (measured
// on Linux and macOS), and so does a file that is not JSON (measured on macOS). A file
// that is not a JSON object and a file that cannot be read count as none too
// (claustrum's own, not measured).
func policyHoldsOtherKey() bool {
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
