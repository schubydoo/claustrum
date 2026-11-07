//go:build !linux

package main

// hostTermSiblingSparesLegacyOnly is true off Linux: a live sibling run dir of the
// host-term shape spares only the shared legacy root, and the root of this install
// is pruned (c9125eb8, measured on macOS and Windows).
const hostTermSiblingSparesLegacyOnly = true
