package main

import (
	"io/fs"
	"net"
	"strings"
	"syscall"
	"testing"
)

// readRealPassLine is the start line of the files handler after a good start test.
const readRealPassLine = "INFO  [FilesHandler] files.realpath: using the OS's path for the open handle\n"

// TestReadRealLineWithNoGate: a plain start logs the start line of the files
// handler at once (measured on Linux, 4c111e86). The log capture is shared, so
// the test counts only that line.
func TestReadRealLineWithNoGate(t *testing.T) {
	stubStartTest(t, okLink, okInfo)
	logs := captureLogBuf(t)
	var g *peerGate
	g.logReadRealStart()
	if n := strings.Count(logs.String(), readRealPassLine); n != 1 {
		t.Errorf("the start line appears %d times, want 1. Log:\n%s", n, logs.String())
	}
}

// TestReadRealLineHeldUntilTheLift: after a failed start naming of the peer check,
// the start line of the files handler waits. It comes once, right after the
// serving line of the first good read (4c111e86, measured on Linux). A good start
// naming logs it at once (measured on Linux). The test reads only the lines of
// the start test and the serving line, as the log capture is shared.
func TestReadRealLineHeldUntilTheLift(t *testing.T) {
	t.Setenv(peerCheckEnv, "1")
	stubStartTest(t, okLink, okInfo)
	nameErr := error(ownNamingError{&fs.PathError{Op: "readlink", Path: "/proc/self/ns/user", Err: syscall.ENFILE}})
	old := readDaemonNSText
	t.Cleanup(func() { readDaemonNSText = old })
	readDaemonNSText = func() (nsText, error) {
		if nameErr != nil {
			return nsText{}, nameErr
		}
		return measuredOwn, nil
	}
	oldLook := lookupPeerOf
	t.Cleanup(func() { lookupPeerOf = oldLook })
	lookupPeerOf = func(net.Conn, nsText) (nsText, bool, error) { return measuredOwn, false, nil }
	logs := captureLogBuf(t)
	g := startPeerCheck()
	g.logReadRealStart()
	if ok, _ := peerAdmitOnce(t, g); ok {
		t.Fatal("the caller before a good read was served")
	}
	if strings.Contains(logs.String(), readRealPassLine) {
		t.Errorf("the start line came before the lift. Log:\n%s", logs.String())
	}
	nameErr = nil
	if ok, _ := peerAdmitOnce(t, g); !ok {
		t.Fatal("the caller after a good read was refused")
	}
	if ok, _ := peerAdmitOnce(t, g); !ok {
		t.Fatal("the second caller after a good read was refused")
	}
	serving := `INFO  [Server] serving only callers in "user:[4026531837] net:1 pidfd"` + "\n"
	out := logs.String()
	if n := strings.Count(out, readRealPassLine); n != 1 || !strings.Contains(out, serving+readRealPassLine) {
		t.Errorf("want the start line once, right after the serving line. Log:\n%s", out)
	}

	// A good start naming writes the line at once.
	g2 := startPeerCheck()
	before := strings.Count(logs.String(), readRealPassLine)
	g2.logReadRealStart()
	if n := strings.Count(logs.String(), readRealPassLine); n != before+1 {
		t.Errorf("after a good start naming the start line appears %d more times, want 1", n-before)
	}
}
