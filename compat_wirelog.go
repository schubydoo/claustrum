package main

import "github.com/schubydoo/claustrum/internal/wirelog"

// The names of internal/wirelog under the names that the code of package main
// uses.

type (
	wireLog        = wirelog.Log
	wireLogOptions = wirelog.Options
)

const wireLogMaxString = wirelog.MaxString

func openWireLog(opt wireLogOptions) (*wireLog, error) { return wirelog.Open(opt) }
