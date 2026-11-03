package main

import "github.com/schubydoo/claustrum/internal/runlock"

// The names of internal/runlock under the names that the code of package main
// uses.

const runDirNotHolderLine = runlock.NotHolderLine

const (
	stopWordStopped    = runlock.StopWordStopped
	stopWordNone       = runlock.StopWordNone
	stopWordTerminated = runlock.StopWordTerminated
	stopWordKilled     = runlock.StopWordKilled
	stopWordSurvivor   = runlock.StopWordSurvivor
)

func lockRunFolder(socket, role string) runlock.Claim { return runlock.LockRunFolder(socket, role) }

func stopRunDirHolder(socket string) (string, func()) { return runlock.StopRunDirHolder(socket) }

func stopAfterReply(socket string) (string, func()) { return runlock.StopAfterReply(socket) }
