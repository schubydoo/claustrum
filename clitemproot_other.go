//go:build !linux && !darwin

package main

import "github.com/schubydoo/claustrum/internal/rpc"

// privateTempRoot adds nothing on Windows: b3081b51 accepts the flag there and
// answers {"success":true} with no folder (measured on Windows).
func privateTempRoot(env []string, runDir string) ([]string, *rpc.CLITempRoot, func(), func(end func())) {
	return env, nil, func() {}, func(func()) {}
}
