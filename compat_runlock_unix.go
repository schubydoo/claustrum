//go:build unix

package main

import "github.com/schubydoo/claustrum/internal/runlock"

// The unix names of internal/runlock under the names that the code of package main
// uses.

const runDirLockName = runlock.LockName

type ownerRecord = runlock.OwnerRecord
