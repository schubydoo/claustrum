package main

import "os"

// peerCheckEnv is the variable of the daemon's own environment that sets the
// peerCheck member. On 5fd08069 it sets that member too (Linux, macOS and Windows
// VMs). The launcher hands its whole environment to the daemonized child
// (daemonizeWithToken), so the variable reaches the daemon. The daemon reads it
// and then removes it from its own environment (startPeerCheck).
const peerCheckEnv = "CLAUDE_SSH_PEER_CHECK"

// The values of the peerCheck member of server.capabilities. 5fd08069 has a third
// value, "on", with a peerCheckBy member, on Linux with the variable at 1 (Linux
// VM). claustrum has no peer check and answers neither (D25).
const (
	peerCheckOff         = "off"
	peerCheckUnavailable = "unavailable"
)

// peerCheckAsked tells if the variable has the exact text "1". On 5fd08069 each of
// these gave "off". Linux VM: no variable, "0", "off", the empty text, "true",
// "yes", "on", "2", "01" and " 1". macOS VM: no variable, "0", "off", the empty
// text and "true". Windows VM: no variable, "0" and "off".
func peerCheckAsked() bool {
	return os.Getenv(peerCheckEnv) == "1"
}

// unsetenv is os.Unsetenv. A test replaces it to see the line of a failed removal.
var unsetenv = os.Unsetenv

// startPeerCheck reads the variable once, at the start of the daemon, and then
// removes it from the daemon's own environment, with any value. So no child of
// the daemon inherits it: no process.spawn child, no git call, no launcher run and
// no login-shell read. With the value 1 it writes one line and returns true. The
// daemon then serves every caller, on every system.
//
// On 5fd08069 a process.spawn child had no such entry for the daemon values 1 and
// 0 (Linux, macOS and Windows VMs). The git calls of one git.info had none for the
// value 0 (Linux VM). A marker variable of the daemon arrived in each of them. The
// start environment of that daemon process still showed the variable (Linux and
// macOS VMs). The launcher runs and the login-shell read of 5fd08069 are not
// measured. A spawn env param that names the variable reaches the child, as on
// 5fd08069 (Linux, macOS and Windows VMs).
//
// A removal that fails gets one WARN line, claustrum's own. The children of the
// daemon then inherit the variable.
//
// The launcher never calls this, so it hands the variable to the daemonized child.
//
// The WARN level is claustrum's own. From the code, the line comes after the lines
// of the run-dir claim and of the reap, and before the listening line. On 5fd08069
// the line of macOS and Windows came directly before the listening line. Its place
// among the lines of a reap or of a failed token write was not measured.
func startPeerCheck() bool {
	asked := peerCheckAsked()
	if err := unsetenv(peerCheckEnv); err != nil {
		logWarnf("[Server] cannot remove %s from the daemon environment (%v): child processes inherit it", peerCheckEnv, err)
	}
	if !asked {
		return false
	}
	logWarnf("%s", peerCheckUnavailableLine)
	return true
}

// peerCheckAnswer is the peerCheck member of server.capabilities.
func (s *server) peerCheckAnswer() string {
	if s.peerCheckAsked {
		return peerCheckUnavailable
	}
	return peerCheckOff
}
