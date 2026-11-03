package main

import "github.com/schubydoo/claustrum/internal/pathcanon"

// The names of internal/pathcanon under the names that the code of package main
// uses.

func canonicalPath(p string) string { return pathcanon.Canonical(p) }

func finalDirPath(p string) string { return pathcanon.FinalDir(p) }

func sameCanonicalPath(a, b string) bool { return pathcanon.Same(a, b) }

func expandPath(p string) string { return pathcanon.ExpandTilde(p) }
