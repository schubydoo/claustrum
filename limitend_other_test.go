//go:build !linux

package main

import (
	"testing"
	"time"
)

// TestLimitEndOffOutsideLinux: off Linux no end at a limit runs, also with a limit set
// (13047a51 reads no host settings file on macOS or Windows, measured).
func TestLimitEndOffOutsideLinux(t *testing.T) {
	s := &server{hostLimit: hostLimit{path: "settings.json", minutes: 15}}
	if s.limitEnd(0, 100*time.Hour) {
		t.Fatal("the end at the limit ran off Linux")
	}
}
