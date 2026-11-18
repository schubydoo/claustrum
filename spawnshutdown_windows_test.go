//go:build windows

package main

import "testing"

// endLeftChild has nothing to end on Windows: the spawn ended the child of its late
// start itself.
func endLeftChild(*testing.T, int) {}
