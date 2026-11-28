package install

import (
	"encoding/json"
	"errors"
	"io/fs"
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
// gets not_absolute (measured on Linux). On Windows each request with a launcherCheck
// comes here, with or without p, and gets the Windows refusal (4c111e86, measured on
// Windows).
//
// orManagedFolder is the orManagedFolder member of the launcherCheck (13047a51). With
// it, a p of managedFolderNamed that names nothing gets the check of the launcher in
// the managed-settings folder (checkManagedFolderLauncher).
func ResolveChecked(p string, chk LauncherCheck, orManagedFolder bool) rpc.LauncherCheckReply {
	res := rpc.LauncherCheckReply{HostPolicySeen: policyHoldsOtherKey()}
	fault, reason := LauncherCheckHostRefusal()
	switch {
	case fault != "":
	case orManagedFolder && p == managedFolderNamed && nothingAt(p):
		res.Checked = managedFolderLauncher
		fault, reason = checkManagedFolderLauncher(chk)
	default:
		fault, reason = CheckLauncherFile(p, chk)
	}
	if fault != "" {
		res.Status, res.Reason, res.Fault = StatusUnusable, reason, fault
		return res
	}
	res.Status, res.Argv = managedStatusUsable, []string{p}
	return res
}

// The two paths of an orManagedFolder check (13047a51, measured on Linux).
// managedFolderNamed is the path that the request names, and managedFolderLauncher is
// the launcher in the managed-settings folder. Seams, so a test points them at a temp
// folder. Production never reassigns them. macOS and Windows refuse before they count.
var (
	managedFolderNamed    = "/etc/claude-desktop-ssh/launcher"
	managedFolderLauncher = "/etc/claude-code/desktop-ssh-launcher"
)

// nothingAt reports that no entry has the name p. 13047a51 checks the managed folder
// when p is absent (measured on Linux). A link, a folder or a refused file at p
// keeps the check on p (claustrum's own, not measured).
func nothingAt(p string) bool {
	_, err := os.Lstat(p)
	return errors.Is(err, fs.ErrNotExist)
}

// checkManagedFolderLauncher runs the file check on managedFolderLauncher. Its texts
// follow 13047a51 (measured on Linux): the writable text of the walk, an owner text of
// its own for the uid of the login, and for a missing file or a folder the text of
// the walk with a second sentence. The other faults keep the texts of the walk
// (claustrum's own, not measured).
func checkManagedFolderLauncher(chk LauncherCheck) (fault, reason string) {
	fault, reason = checkLauncherPath(managedFolderLauncher, chk, true)
	if fault == FaultMissing && reason == managedNotExecutable(managedFolderLauncher) {
		reason += ". Nothing lies at `" + managedShown(managedFolderNamed) + "` either."
	}
	return fault, reason
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
