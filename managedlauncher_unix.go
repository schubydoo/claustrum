//go:build !windows

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// managedSystemSettingsDir is the system managed-settings folder of this OS. A var
// only so a test points it at a temp folder. Production never reassigns it.
var managedSystemSettingsDir = managedSettingsSystemDir

// managedSettingsDir is the managed-settings folder. The reference reads the system
// folder first. When it holds this host's policy, the E2E variable is ignored and
// the system folder is read (row RXs on Linux and macOS). The folder holds a policy
// when it has the base file (RXs) or a drop-in (row RXd, Linux). An existing but
// empty system folder does not count (row RXe, Linux). With the system folder
// absent, the variable selects its folder (row RX, Linux and macOS). An empty value
// counts as unset, and the system folder is read (row RY, Linux and macOS). A
// hidden .json entry alone counts too, although the resolve skips it (row RXd2,
// Linux). The second return is true when the variable was set and ignored, so the
// caller logs the line.
func managedSettingsDir() (dir string, ignoredVar bool) {
	d := os.Getenv(managedSettingsDirEnv)
	if d == "" {
		return managedSystemSettingsDir, false
	}
	if managedSystemFolderHoldsPolicy() {
		return managedSystemSettingsDir, true
	}
	return d, false
}

// managedSystemFolderHoldsPolicy reports whether the system folder holds a policy.
// The base file counts when its path exists (os.Lstat), a dangling symlink too. A
// managed-settings.d counts when it holds an entry whose name ends in ".json" and
// that is a file or a symlink, a dangling symlink too. A hidden name counts (row
// RXd2, Linux). claustrum's choices (not measured) follow. A non-.json entry and a
// folder entry do not count, as for the drop-in rule of the resolve. The dangling
// symlinks count. A folder that cannot be listed does not count.
func managedSystemFolderHoldsPolicy() bool {
	if _, err := os.Lstat(filepath.Join(managedSystemSettingsDir, managedSettingsBase)); err == nil {
		return true
	}
	ents, err := readManagedDropInDir(filepath.Join(managedSystemSettingsDir, managedSettingsDropIns))
	if err != nil {
		return false
	}
	for _, e := range ents {
		t := e.Type()
		if strings.HasSuffix(e.Name(), ".json") && (t&fs.ModeSymlink != 0 || t.IsRegular()) {
			return true
		}
	}
	return false
}

// managedIgnoredVarLine is the log line of a resolve that ignored the E2E variable.
// The system folder held this host's policy (row RXs, measured on Linux and macOS). The path
// is the system folder of the OS.
func managedIgnoredVarLine() string {
	return "[launcher] " + managedSettingsDirEnv + " ignored: " + managedSystemSettingsDir + " holds this host's policy"
}

// managedLauncherHostRefusal is empty on unix, where a launcher applies.
func managedLauncherHostRefusal() string { return "" }
