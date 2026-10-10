package main

import "os"

// peerCheckEnv is the variable of the daemon's own environment that asks for the
// peer check. On 5fd08069 it sets the peerCheck member (Linux, macOS and Windows
// VMs). The launcher hands its whole environment to the daemonized child
// (daemonizeWithToken), so the variable reaches the daemon.
const peerCheckEnv = "CLAUDE_SSH_PEER_CHECK"

// The values of the peerCheck member of server.capabilities. 5fd08069 has a third
// value, "on", with a peerCheckBy member, on Linux with the variable at 1 (Linux
// VM). claustrum has no peer check and answers neither.
const (
	peerCheckOff         = "off"
	peerCheckUnavailable = "unavailable"
)

// peerCheckAsked tells if the environment asks for the peer check. The exact text
// "1" asks for it. On 5fd08069 each of these gave "off". Linux VM: no variable,
// "0", the empty text, "true", "yes", "on", "2", "01" and " 1". macOS VM: no
// variable, "0", "off", the empty text and "true". Windows VM: no variable, "0"
// and "off".
func peerCheckAsked() bool {
	return os.Getenv(peerCheckEnv) == "1"
}

// startPeerCheck reads the variable once, at the start of the daemon. With the
// value 1 it writes one line and returns true. The daemon then serves every
// caller, on every system.
//
// The WARN level is claustrum's own. From the code, the line comes after the lines
// of the run-dir claim and of the reap, and before the listening line. On 5fd08069
// the line of macOS and Windows came directly before the listening line. Its place
// among the lines of a reap or of a failed token write was not measured.
func startPeerCheck() bool {
	if !peerCheckAsked() {
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
