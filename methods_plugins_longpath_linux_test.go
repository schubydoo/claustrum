//go:build linux

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// On Linux the sibling test of plugins.prune dials a 120-byte sibling through /proc
// (`be1c1ca5`, measured on Linux). A dead one counts as gone, so the legacy sweep runs.
// A live one answers.
func TestSiblingAtALongPathThroughProc(t *testing.T) {
	base, sock := longSibling(t)
	if got := siblingDaemonAlive(base, "abcd1234"); got != "" {
		t.Errorf("dead long sibling: got %q, want it counted as gone", got)
	}
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	ln, err := listenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if got, want := siblingDaemonAlive(base, "abcd1234"), "run/beef5678/rpc.sock answers"; got != want {
		t.Errorf("live long sibling: got %q, want %q", got, want)
	}
}

// A sibling socket of mode 000 and a sibling folder of mode 0600 at 120 bytes keep the
// legacy dirs, with the texts of `be1c1ca5` (measured on Linux).
func TestSiblingAtALongPathPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the modes")
	}
	base, sock := longSibling(t)
	if err := os.Chmod(sock, 0); err != nil {
		t.Fatal(err)
	}
	want := regexp.MustCompile(`^cannot rule out run/beef5678/rpc\.sock: dial unix ` + regexp.QuoteMeta(sock) +
		`: the socket path is longer than the 107 bytes that this system allows; no shorter name for it worked: ` +
		`dial unix /proc/self/fd/[0-9]+/rpc\.sock: connect: permission denied$`)
	if got := siblingDaemonAlive(base, "abcd1234"); !want.MatchString(got) {
		t.Errorf("socket of mode 000: got %q, want %s", got, want)
	}

	dir := filepath.Dir(sock)
	if err := os.Chmod(dir, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	wantDir := "cannot inspect " + sock + ": stat " + sock + ": permission denied"
	if got := siblingDaemonAlive(base, "abcd1234"); got != wantDir {
		t.Errorf("folder of mode 0600: got %q, want %q", got, wantDir)
	}
}
