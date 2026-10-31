//go:build windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// denyReadAndExecute puts a deny ACE for read and execute for the user of this
// process on dir, a folder or a file, as `icacls <dir> /deny <user>:(RX)` does. It gives a function
// that puts the old DACL back. A cleanup runs it too, before the cleanup of
// t.TempDir, so the temp folder can go.
func denyReadAndExecute(t *testing.T, dir string) (restore func()) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	oldACL, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	deny := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE,
		AccessMode:        windows.DENY_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}
	newACL, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{deny}, oldACL)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, newACL, nil); err != nil {
		t.Fatal(err)
	}
	restore = func() {
		_ = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, oldACL, nil)
	}
	t.Cleanup(restore)
	// The instrument test: the ACE must stop this process from opening dir. On
	// CI a token that the ACE does not stop fails the test, as in
	// TestFilesListWindowsLinks.
	f, err := os.Open(dir)
	if err == nil {
		_ = f.Close()
	}
	if !errors.Is(err, fs.ErrPermission) {
		restore()
		if os.Getenv("CI") != "" {
			t.Fatalf("the deny ACE does not stop this token from opening %s (err %v)", dir, err)
		}
		t.Skipf("the deny ACE does not stop this token from opening the entry (err %v)", err)
	}
	return restore
}

// TestFilesExtractTarRemovesUnreadableEmptyDestDir: an empty destDir with a deny
// ACE for read and execute goes, and the archive is unpacked into a new folder.
// 683cc6a9 answers success with fileCount 1 there, and the new folder has no
// deny ACE (Windows VM).
func TestFilesExtractTarRemovesUnreadableEmptyDestDir(t *testing.T) {
	s := newTestServer(t)
	parent := t.TempDir()
	dest := filepath.Join(parent, "0123456789abcdef")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	denyReadAndExecute(t, dest)

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": oneFileArchive(t), "destDir": dest}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"fileCount":1}}`; got != want {
		t.Errorf("frame = %s, want %s", got, want)
	}
	if got := namesIn(t, parent); got != "0123456789abcdef" {
		t.Errorf("the parent holds %q, want destDir alone", got)
	}
	if got := namesIn(t, dest); got != ".synced,a.txt" {
		t.Errorf("destDir holds %q, want the marker and a.txt", got)
	}
}

// TestFilesExtractTarKeepsUnreadableDestDirWithContent: the same deny ACE on a
// destDir with content. 683cc6a9 answers the failure below, and the content and
// the ACE stay (Windows VM).
func TestFilesExtractTarKeepsUnreadableDestDirWithContent(t *testing.T) {
	s := newTestServer(t)
	parent := t.TempDir()
	dest := filepath.Join(parent, "0123456789abcdef")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "keep"), []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := denyReadAndExecute(t, dest)

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": oneFileArchive(t), "destDir": dest}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"fileCount":0,"error":"clean destDir: RemoveAll 0123456789abcdef: Access is denied."}}`; got != want {
		t.Errorf("frame = %s, want %s", got, want)
	}
	if _, err := os.ReadDir(dest); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("listing destDir after the request = %v, want the deny ACE to stay", err)
	}
	restore()
	if got := namesIn(t, dest); got != "keep" {
		t.Errorf("destDir holds %q, want keep alone", got)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "keep")); err != nil || string(b) != "k" {
		t.Errorf("keep = %q (err %v), want it unchanged", b, err)
	}
}

// TestFilesExtractTarKeepsUnreadableFileAtDestDir: a regular file at the name of
// destDir with the same deny ACE. The remove of an unreadable empty folder does
// not take a file, so the request fails and the file stays (claustrum's own, not
// measured).
func TestFilesExtractTarKeepsUnreadableFileAtDestDir(t *testing.T) {
	s := newTestServer(t)
	parent := t.TempDir()
	dest := filepath.Join(parent, "0123456789abcdef")
	if err := os.WriteFile(dest, []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := denyReadAndExecute(t, dest)

	got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar", map[string]any{"archivePath": oneFileArchive(t), "destDir": dest}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"fileCount":0,"error":"clean destDir: RemoveAll 0123456789abcdef: Access is denied."}}`; got != want {
		t.Errorf("frame = %s, want %s", got, want)
	}
	restore()
	if b, err := os.ReadFile(dest); err != nil || string(b) != "k" {
		t.Errorf("the file at destDir = %q (err %v), want it unchanged", b, err)
	}
}
