//go:build windows

package main

// defaultShellAgentSocket never probes on Windows. The capability and the
// disableShellAgentSocket param exist on every OS, but claustrum's Windows spawn
// adds no SSH_AUTH_SOCK and runs no login shell.
func defaultShellAgentSocket() string { return "" }
