//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestReadHostLimitFileKinds: the read of the host settings file by its kind and size
// (13047a51, measured on Linux: no file, a folder, a link, 64 KiB and 64 KiB+1, mode 0600
// for an account that is not root). The FIFO row is claustrum's own: the read does not wait.
func TestReadHostLimitFileKinds(t *testing.T) {
	otherOwner(t)
	dir := t.TempDir()
	body := `{"unattendedLimitMinutes": 30}`
	write := func(name, s string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	file := write("settings.json", body, 0o644)
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, "folder.json")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	pad := `{"unattendedLimitMinutes": 30, "p": "`
	full := write("full.json", pad+strings.Repeat("a", hostLimitMaxSize-len(pad)-2)+`"}`, 0o644)
	over := write("over.json", pad+strings.Repeat("a", hostLimitMaxSize-len(pad)-1)+`"}`, 0o644)
	fifo := filepath.Join(dir, "fifo.json")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		path    string
		minutes int
		detail  string
	}{
		{"absent", filepath.Join(dir, "none", "settings.json"), 0, ""},
		{"file", file, 30, ""},
		{"link", link, 30, ""},
		{"size64k", full, 30, ""},
		{"size64k_plus1", over, 0, hostDetailTooLarge},
		{"dir", folder, 0, hostDetailNotFile},
		{"fifo", fifo, 0, hostDetailNotFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan hostLimit, 1)
			go func() { done <- readHostLimit(tc.path) }()
			var h hostLimit
			select {
			case h = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the read still waits after 10 s")
			}
			if h.minutes != tc.minutes || h.detail != tc.detail {
				t.Errorf("got minutes %d, detail %q, want %d, %q", h.minutes, h.detail, tc.minutes, tc.detail)
			}
			if tc.detail != "" && h.fault != hostFaultUnreadable {
				t.Errorf("fault %q, want %q", h.fault, hostFaultUnreadable)
			}
		})
	}
	t.Run("noread", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a file of mode 0000")
		}
		p := write("closed.json", body, 0o000)
		h := readHostLimit(p)
		if h.fault != hostFaultUnreadable || h.detail != hostDetailNoRead {
			t.Errorf("got fault %q, detail %q", h.fault, h.detail)
		}
	})
}

// TestReadHostLimitAtStartUsesThePath: the start read takes hostLimitPath and logs its one
// line with that path.
func TestReadHostLimitAtStartUsesThePath(t *testing.T) {
	otherOwner(t)
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"unattendedLimitMinutes": 60}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := hostLimitPath
	hostLimitPath = p
	t.Cleanup(func() { hostLimitPath = old })
	var h hostLimit
	logs := captureLog(t, func() { h = readHostLimitAtStart() })
	if h.minutes != 60 {
		t.Errorf("minutes %d, want 60", h.minutes)
	}
	if want := "INFO  [Server] " + p + " sets an unattended limit of 60 minutes"; !strings.HasPrefix(logs, want) {
		t.Errorf("log %q, want the start %q", logs, want)
	}
}

// otherOwner makes the files of this test count as files of another account, so the
// owner rule passes them (claustrum's own reading for another account).
func otherOwner(t *testing.T) {
	t.Helper()
	old := hostLimitOwnUID
	hostLimitOwnUID = func() int { return os.Getuid() + 1 }
	t.Cleanup(func() { hostLimitOwnUID = old })
}

// TestHostFileRules: the owner and mode rules of the file and its folder, with the texts of
// 13047a51 (measured on Linux for the modes and the owner below).
func TestHostFileRules(t *testing.T) {
	body := []byte(`{"unattendedLimitMinutes": 30}`)
	mk := func(t *testing.T, dirMode, fileMode os.FileMode) (string, string) {
		dir := filepath.Join(t.TempDir(), "claude-desktop-ssh")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "settings.json")
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, fileMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, dirMode); err != nil {
			t.Fatal(err)
		}
		return dir, p
	}
	t.Run("pass", func(t *testing.T) {
		otherOwner(t)
		_, p := mk(t, 0o755, 0o644)
		if h := readHostLimit(p); h.fault != "" || h.minutes != 30 {
			t.Errorf("got fault %q, minutes %d", h.fault, h.minutes)
		}
	})
	for _, tc := range []struct {
		name  string
		dir   os.FileMode
		file  os.FileMode
		mode  string
		ofDir bool
	}{
		{"mode666", 0o755, 0o666, "0666", false},
		{"mode664", 0o755, 0o664, "0664", false},
		{"dir777", 0o777, 0o644, "0777", true},
		// An other write bit with no group write bit (measured on Linux for 0646 and 0757).
		{"mode646", 0o755, 0o646, "0646", false},
		{"dir757", 0o757, 0o644, "0757", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			otherOwner(t)
			dir, p := mk(t, tc.dir, tc.file)
			named := p
			if tc.ofDir {
				named = dir
			}
			want := "`" + named + "` can be changed by accounts other than its owner (mode " + tc.mode + "). Remove that write access."
			if h := readHostLimit(p); h.fault != hostFaultWritable || h.detail != want {
				t.Errorf("got fault %q, detail %q, want %q", h.fault, h.detail, want)
			}
		})
	}
	// A file that the daemon cannot read and others can change gives the write text, as
	// a root file of mode 0642 does on 13047a51 (measured on Linux). Mode 0242 makes the
	// file of the test account unreadable to it.
	t.Run("unreadable_writable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a file of any mode")
		}
		otherOwner(t)
		_, p := mk(t, 0o755, 0o242)
		want := "`" + p + "` can be changed by accounts other than its owner (mode 0242). Remove that write access."
		if h := readHostLimit(p); h.fault != hostFaultWritable || h.detail != want {
			t.Errorf("got fault %q, detail %q, want %q", h.fault, h.detail, want)
		}
	})
	t.Run("owner_login", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("a root file passes the owner rule")
		}
		_, p := mk(t, 0o755, 0o644)
		want := "`" + p + "` belongs to the account that signs in (uid " + strconv.Itoa(os.Getuid()) + "). Root must own the settings file and every folder above it."
		if h := readHostLimit(p); h.fault != hostFaultOwner || h.detail != want {
			t.Errorf("got fault %q, detail %q, want %q", h.fault, h.detail, want)
		}
	})
}
