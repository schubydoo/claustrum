//go:build unix

package main

import (
	"syscall"
	"testing"
)

func TestParseSignal(t *testing.T) {
	cases := []struct {
		in   string
		want syscall.Signal
	}{
		{"KILL", syscall.SIGKILL},
		{"kill", syscall.SIGKILL},    // ToUpper handles case for the bare name
		{"SIGKILL", syscall.SIGKILL}, // SIG prefix stripped (case-sensitive), then matched
		{"sigkill", syscall.SIGTERM}, // quirk: TrimPrefix is case-sensitive, so lowercase "sig" isn't stripped → default
		{"INT", syscall.SIGINT},
		{"HUP", syscall.SIGHUP},
		{"QUIT", syscall.SIGQUIT},
		{"TERM", syscall.SIGTERM},
		{"", syscall.SIGTERM},      // default
		{"bogus", syscall.SIGTERM}, // unknown → default
	}
	for _, tc := range cases {
		if got := parseSignal(tc.in); got != tc.want {
			t.Errorf("parseSignal(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestDetachSysProcAttr(t *testing.T) {
	if detachSysProcAttr() == nil {
		t.Error("detachSysProcAttr should return a non-nil SysProcAttr")
	}
}
