package main

import (
	"github.com/schubydoo/claustrum/internal/git"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// The names of internal/git under the names that the code of package main
// uses.

type (
	gitDeps    = git.Deps
	shadowHost = git.ShadowHost
)

func newShadowHost(runDir string) *shadowHost { return git.NewShadowHost(runDir) }

func handleGit(d *gitDeps, r *rpc.Request) response { return git.Handle(d, r) }
