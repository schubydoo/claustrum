package main

import "github.com/schubydoo/claustrum/internal/install"

// The names of internal/install under the names that the code of package main
// uses.

func stripManagedLauncherEnv(env []string, launched bool) []string {
	return install.StripEnv(env, launched)
}
