package main

import "github.com/schubydoo/claustrum/internal/hostsweep"

// The name of internal/hostsweep under the name that the code of package main uses.

func startHostCleaner(socket string) { hostsweep.Start(socket) }
