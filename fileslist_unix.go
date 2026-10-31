//go:build !windows

package main

import "os"

// listIsDir decides isDir of one files.list entry. The target of a link decides
// it, and junctionsAsDirs changes nothing (build 683cc6a9, Linux and macOS).
func listIsDir(full string, _ bool) bool {
	fi, err := os.Stat(full)
	return err == nil && fi.IsDir()
}
