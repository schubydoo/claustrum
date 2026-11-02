//go:build !windows

package install

import "testing"

// TestSetSystemSettingsDirForTestRestores: the hook sets the folder, and the
// returned func puts the old one back.
func TestSetSystemSettingsDirForTestRestores(t *testing.T) {
	old := managedSystemSettingsDir
	restore := SetSystemSettingsDirForTest("/fake/system")
	during := managedSystemSettingsDir
	restore()
	if during != "/fake/system" || managedSystemSettingsDir != old {
		t.Errorf("folder = %q during, %q after; want /fake/system, then %q", during, managedSystemSettingsDir, old)
	}
}
