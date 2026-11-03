//go:build linux

package procinfo

// Root returns the /proc mount that the reads of package main use.
func Root() string { return procRoot }

// Readlink reads the link name through the osReadlink seam. The pid-namespace test of
// the run-dir lock uses it.
func Readlink(name string) (string, error) { return osReadlink(name) }
