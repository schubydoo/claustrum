package main

import "github.com/schubydoo/claustrum/internal/supervisor"

// The spawn of internal/supervisor runs two steps of this package: the private temp
// folder (clitemproot_linux.go) and the session folders of a bare command name
// (sessionfolders.go).
func init() { supervisor.SetSpawnSteps(privateTempRoot, bareCommandStart) }
