package install

import (
	"errors"
	"io/fs"
	"testing"
)

// TestSetReadDropInDirForTestRestores: the hook lists through f, and the
// returned func puts the old listing back.
func TestSetReadDropInDirForTestRestores(t *testing.T) {
	errFake := errors.New("fake listing")
	restore := SetReadDropInDirForTest(func(string) ([]fs.DirEntry, error) { return nil, errFake })
	_, during := readManagedDropInDir(t.TempDir())
	restore()
	_, after := readManagedDropInDir(t.TempDir())
	if !errors.Is(during, errFake) || after != nil {
		t.Errorf("listing error = %v during, %v after; want the fake, then nil", during, after)
	}
}

// TestSetReadSettingsFileForTestRestores: the hook reads through f, and the
// returned func puts the old read back.
func TestSetReadSettingsFileForTestRestores(t *testing.T) {
	errFake := errors.New("fake read")
	missing := t.TempDir() + "/missing.json"
	restore := SetReadSettingsFileForTest(func(string) ([]byte, error) { return nil, errFake })
	_, during := readManagedSettingsFile(missing)
	restore()
	_, after := readManagedSettingsFile(missing)
	if !errors.Is(during, errFake) || !errors.Is(after, fs.ErrNotExist) {
		t.Errorf("read error = %v during, %v after; want the fake, then not exist", during, after)
	}
}
