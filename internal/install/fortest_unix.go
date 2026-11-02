//go:build !windows

package install

// SetSystemSettingsDirForTest points the system managed-settings folder at dir.
// Only tests of package main call it. The returned func puts the old folder
// back.
func SetSystemSettingsDirForTest(dir string) (restore func()) {
	old := managedSystemSettingsDir
	managedSystemSettingsDir = dir
	return func() { managedSystemSettingsDir = old }
}
