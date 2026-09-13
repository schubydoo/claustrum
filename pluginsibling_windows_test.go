//go:build windows

package main

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

// TestExtraNobodyListeningWindows pins the Windows-only branch of the sibling-dial
// classification: a refused Winsock dial (WSAECONNREFUSED) means the sibling is
// gone, while a timeout or a generic error leaves it possibly present. A mutant
// whose extraNobodyListening returned false for WSAECONNREFUSED would let a refused
// sibling on Windows skip the legacy sweep, diverging from the reference.
func TestExtraNobodyListeningWindows(t *testing.T) {
	if !extraNobodyListening(windows.WSAECONNREFUSED) {
		t.Error("WSAECONNREFUSED must be nobody-listening on Windows")
	}
	if extraNobodyListening(windows.WSAETIMEDOUT) {
		t.Error("WSAETIMEDOUT must not be nobody-listening")
	}
	if extraNobodyListening(errors.New("boom")) {
		t.Error("a generic error must not be nobody-listening")
	}
}
