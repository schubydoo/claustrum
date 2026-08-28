//go:build windows

package main

import "os"

// livePredecessorIdent is a nil stub on Windows. The dial-based predecessor probe has
// no observable effect there: a second launch over a live predecessor returns in
// ~0.01s with both daemons coexisting, the same whether or not the probe runs, and the
// same as the reference (measured against 7d193f89). So claustrum skips the probe on
// Windows and waitForDaemonAccept takes its pred==nil path — returning as soon as the
// socket is present. (isSocketDead / isConnRefused are therefore unreachable in
// production on Windows; they stay for the unix path and TestIsConnRefusedStaleSocket.)
func livePredecessorIdent(socket string) os.FileInfo {
	return nil
}
