package install

import "io/fs"

// The functions below let the tests of package main fake the reads of the
// managed settings. Only tests call them. Each returns a func that puts the
// old read back.

// SetReadDropInDirForTest makes f list the drop-in folder.
func SetReadDropInDirForTest(f func(string) ([]fs.DirEntry, error)) (restore func()) {
	old := readManagedDropInDir
	readManagedDropInDir = f
	return func() { readManagedDropInDir = old }
}

// SetReadSettingsFileForTest makes f read each settings file.
func SetReadSettingsFileForTest(f func(string) ([]byte, error)) (restore func()) {
	old := readManagedSettingsFile
	readManagedSettingsFile = f
	return func() { readManagedSettingsFile = old }
}
