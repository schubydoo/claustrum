//go:build darwin

package main

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

func TestNodeIDDarwin(t *testing.T) {
	got := nodeID()
	if got == "" {
		t.Fatal("darwin nodeID is empty; sysctl kern.bootsessionuuid should be present")
	}
	if strings.Contains(got, "/") {
		t.Errorf("darwin nodeID %q should be a bare boot-session UUID with no separator", got)
	}
}

// TestParseProcArgs2 decodes a synthetic KERN_PROCARGS2 buffer: argc, the executable
// path, NUL padding, then the argument strings (env follows and is ignored).
func TestParseProcArgs2(t *testing.T) {
	var buf []byte
	argc := make([]byte, 4)
	binary.LittleEndian.PutUint32(argc, 2)
	buf = append(buf, argc...)
	buf = append(buf, "/path/to/claustrum"...)
	buf = append(buf, 0, 0, 0) // exec-path terminator + padding
	buf = append(buf, "claustrum"...)
	buf = append(buf, 0)
	buf = append(buf, "-serve"...)
	buf = append(buf, 0)
	buf = append(buf, "ENV=ignored"...) // env, past argc, must be dropped
	buf = append(buf, 0)

	got := parseProcArgs2(buf)
	if len(got) != 2 || got[0] != "claustrum" || got[1] != "-serve" {
		t.Errorf("parseProcArgs2 = %q, want [claustrum -serve]", got)
	}
}

// TestRealIsServeCmdlineDarwinSelf confirms procArgv reads our own argv via
// KERN_PROCARGS2, and that our test process is not mistaken for a -serve daemon.
func TestRealIsServeCmdlineDarwinSelf(t *testing.T) {
	if _, ok := procArgv(os.Getpid()); !ok {
		t.Fatal("procArgv could not read our own argv via KERN_PROCARGS2")
	}
	if realIsServeCmdline(os.Getpid(), "/run/x/s.sock") {
		t.Error("our own test process must not look like a -serve daemon")
	}
}
