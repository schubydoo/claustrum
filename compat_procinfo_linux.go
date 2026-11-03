//go:build linux

package main

import "github.com/schubydoo/claustrum/internal/procinfo"

// The linux names of internal/procinfo under the names that the code of package main
// uses.

func procRoot() string { return procinfo.Root() }

func osReadlink(name string) (string, error) { return procinfo.Readlink(name) }
