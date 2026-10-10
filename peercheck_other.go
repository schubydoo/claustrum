//go:build !linux

package main

// peerCheckUnavailableLine is the line of a daemon that starts with
// CLAUDE_SSH_PEER_CHECK=1 on a system other than Linux. The text is the one of
// 5fd08069 on macOS and Windows VMs, where it came directly before the listening
// line.
const peerCheckUnavailableLine = "[Server] this system names no namespaces: cannot tell callers in a sandbox from others, serving all"
