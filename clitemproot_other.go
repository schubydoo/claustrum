//go:build !linux

package main

import "github.com/schubydoo/claustrum/internal/rpc"

// privateTempRoot adds nothing on macOS and Windows: b3081b51 accepts the flag there
// and answers {"success":true} with no folder (measured on macOS and Windows).
func privateTempRoot(env []string, runDir string) ([]string, *rpc.CLITempRoot, func(), func(end func())) {
	return env, nil, func() {}, func(func()) {}
}
