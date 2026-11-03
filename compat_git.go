package main

import "github.com/schubydoo/claustrum/internal/git"

// The names of internal/git under the names that the code of package main
// uses.

type (
	shadowHost = git.ShadowHost
)

func newShadowHost(runDir string) *shadowHost { return git.NewShadowHost(runDir) }
