//go:build !darwin

package main

import "time"

// readRealLineWait is zero off macOS: the start line of the files handler does
// not wait for the shell read there.
const readRealLineWait time.Duration = 0
