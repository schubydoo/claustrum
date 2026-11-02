//go:build windows

package main

import "github.com/schubydoo/claustrum/internal/pathcanon"

// The Windows names of internal/pathcanon under the names that the code of
// package main uses.

func onNetwork(p string) bool { return pathcanon.OnNetwork(p) }

func isDriveLetter(c byte) bool { return pathcanon.IsDriveLetter(c) }
