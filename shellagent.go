package main

import "strings"

// agentSockVar is the environment variable that carries the SSH agent socket.
const agentSockVar = "SSH_AUTH_SOCK"

// shellAgentSocket returns the SSH agent socket that the user's login shell
// exports, or "" when there is none to hand on. On linux and darwin it runs the
// login shell at most once per ten minutes and caches the answer (see
// shellagent_unix.go). On Windows it always returns "": the capability and the
// param exist there, but spawn never probes.
//
// A variable so tests can replace it. TestMain stubs it out, so the suite never
// starts a real login shell as a side effect of spawning a fixture.
var shellAgentSocket = defaultShellAgentSocket

// addShellAgentSocket appends SSH_AUTH_SOCK=<login-shell socket> to a child env
// that has no SSH_AUTH_SOCK entry at all, unless the spawn turned the hand-off
// off. An entry that is already present wins even when its value is empty, so a
// caller can opt a single spawn out with "SSH_AUTH_SOCK":"". Measured side by
// side with the reference (f6010b97, macOS and Linux VMs): a daemon-env value, a
// caller value, a caller empty value and disableShellAgentSocket:true each skip
// the login shell on a fresh daemon, and the spawn never fails because of this
// step.
func addShellAgentSocket(env []string, disabled bool) []string {
	if disabled || envContainsKey(env, agentSockVar) {
		return env
	}
	if sock := shellAgentSocket(); sock != "" {
		return append(env, agentSockVar+"="+sock)
	}
	return env
}

// envContainsKey reports whether env holds an entry for key, with any value.
func envContainsKey(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
