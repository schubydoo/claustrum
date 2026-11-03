//go:build unix

package main

import "github.com/schubydoo/claustrum/internal/pathcanon"

// The Linux and macOS names of internal/pathcanon under the names that the code
// of package main uses.

func canonicalPathOfGone(p string) string { return pathcanon.OfGone(p) }
