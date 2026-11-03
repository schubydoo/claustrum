package main

import "github.com/schubydoo/claustrum/internal/pathcanon"

// The names of internal/pathcanon under the names that the code of package main
// uses.

func canonicalPath(p string) string { return pathcanon.Canonical(p) }

func expandPath(p string) string { return pathcanon.ExpandTilde(p) }
