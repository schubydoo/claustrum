//go:build linux

package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// TestResolveCheckedManagedFolder pins the orManagedFolder check of 13047a51
// (measured on Linux, rows F_* of l_e76_launcher). With nothing at the named path,
// the launcher of the managed-settings folder is checked and named in checked. A
// usable one keeps the named path in argv. A named path that is there, another path,
// and a request with no orManagedFolder keep the plain check with no checked.
func TestResolveCheckedManagedFolder(t *testing.T) {
	d := launcherDir(t)
	named := filepath.Join(d, "desktop", "launcher")
	managed := filepath.Join(d, "code", "desktop-ssh-launcher")
	oldNamed, oldManaged := managedFolderNamed, managedFolderLauncher
	managedFolderNamed, managedFolderLauncher = named, managed
	t.Cleanup(func() { managedFolderNamed, managedFolderLauncher = oldNamed, oldManaged })
	t.Setenv(managedSettingsDirEnv, filepath.Join(d, "settings"))
	old := managedSystemSettingsDir
	managedSystemSettingsDir = filepath.Join(d, "absent")
	t.Cleanup(func() { managedSystemSettingsDir = old })
	if err := os.MkdirAll(filepath.Dir(named), 0o755); err != nil {
		t.Fatal(err)
	}
	facts := map[string]launcherFacts{}
	fakeLauncherFacts(t, facts)
	gone := filepath.Join(d, "gone", "launcher")
	missingBoth := "launcher `" + managed + "` does not exist or is not an executable regular file. Nothing lies at `" + named + "` either."
	unusable := func(reason, fault, checked string) rpc.LauncherCheckReply {
		return rpc.LauncherCheckReply{Status: StatusUnusable, Reason: reason, Fault: fault, Checked: checked}
	}
	usable := func(checked string) rpc.LauncherCheckReply {
		return rpc.LauncherCheckReply{Status: managedStatusUsable, Argv: []string{named}, Checked: checked}
	}
	check := func(row, p string, omf bool, want rpc.LauncherCheckReply) {
		t.Helper()
		if got := ResolveChecked(p, LauncherCheck{}, omf); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", row, got, want)
		}
	}
	// F_A0B0: nothing at either path.
	check("A0B0 orManaged", named, true, unusable(missingBoth, FaultMissing, managed))
	check("A0B0 plain", named, false, unusable(managedNotExecutable(named), FaultMissing, ""))
	// A dangling link at the named path keeps the plain check (claustrum's own, not
	// measured): nothingAt reads the link itself.
	if err := os.Symlink(filepath.Join(d, "nowhere"), named); err != nil {
		t.Fatal(err)
	}
	check("dangling link orManaged", named, true, unusable(managedNotExecutable(named), FaultMissing, ""))
	if err := os.Remove(named); err != nil {
		t.Fatal(err)
	}
	// F_Bdir: a folder at the managed path.
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatal(err)
	}
	check("Bdir orManaged", named, true, unusable(missingBoth, FaultMissing, managed))
	if err := os.Remove(managed); err != nil {
		t.Fatal(err)
	}
	// F_A0B1: the managed launcher only.
	writeLauncher(t, managed, "#!/bin/sh\nexit 0\n", 0o755)
	check("A0B1 orManaged", named, true, usable(managed))
	// The member order of the frame (F_A0B1_orManaged).
	b, err := json.Marshal(ResolveChecked(named, LauncherCheck{}, true))
	if want := `{"status":"usable","argv":["` + named + `"],"hostPolicySeen":false,"checked":"` + managed + `"}`; err != nil || string(b) != want {
		t.Errorf("frame %s (%v), want %s", b, err, want)
	}
	check("A0B1 plain", named, false, unusable(managedNotExecutable(named), FaultMissing, ""))
	check("A0B1 gone", gone, true, unusable(managedNotExecutable(gone), FaultMissing, ""))
	// F_B777 and F_Buser.
	facts[managed] = launcherFacts{uid: 0, mode: 0o777}
	check("B777 orManaged", named, true, unusable(writableText(managed, "0777"), FaultWritable, managed))
	facts[managed] = launcherFacts{uid: 1000, mode: 0o755}
	check("Buser orManaged", named, true, unusable("`"+managed+"` belongs to the account that signs in (uid 1000), and no account may use a launcher that it can change itself. Sign in as another account, or make root its owner.", FaultOwner, managed))
	delete(facts, managed)
	// F_A1B1: the named launcher is there.
	writeLauncher(t, named, "#!/bin/sh\nexit 0\n", 0o755)
	check("A1B1 orManaged", named, true, usable(""))
}
