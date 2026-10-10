//go:build linux

package main

// peerCheckUnavailableLine is the line of a Linux daemon that starts with
// CLAUDE_SSH_PEER_CHECK=1. The text is claustrum's own. 5fd08069 runs a peer
// check on Linux and logs another line there (Linux VM). claustrum has no peer
// check, so it says so and serves every caller.
const peerCheckUnavailableLine = "[Server] CLAUDE_SSH_PEER_CHECK is set: this build has no peer check on Linux, serving all callers"
