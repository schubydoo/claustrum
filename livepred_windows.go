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

// staleSocketIdent returns the identity of the socket file that is on disk before the
// launcher starts its child, or nil when there is none. waitForDaemonAccept uses it:
// while the path still holds that file and no daemon answers, the launcher waits. So the
// stale socket of a killed daemon is not taken for the socket of the new one. Measured on
// a Windows VM against f6010b97 and 89cb6289 (rows WN03 and WN05).
//
// On NTFS a file that is deleted and made again at the same path can keep its identity.
// The wait then ends at the first dial that a daemon answers.
func staleSocketIdent(socket string) os.FileInfo {
	fi, err := os.Stat(socket)
	if err != nil {
		return nil
	}
	// On Windows os.Stat does not read the file identity. os.SameFile reads it at the
	// first compare, through the path. This compare reads it now, while the stale file is
	// still the file at that path.
	_ = os.SameFile(fi, fi)
	return fi
}
