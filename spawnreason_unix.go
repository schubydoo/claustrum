//go:build unix

package main

import "golang.org/x/sys/unix"

// cwdUnusable tells if a cwd that exists cannot be entered. It tests the search
// permission of the folder. Measured on Linux and macOS for a folder of mode 000.
func cwdUnusable(cwd string) bool {
	return unix.Access(cwd, unix.X_OK) != nil
}
