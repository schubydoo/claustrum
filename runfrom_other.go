//go:build !linux

package main

// examinePlace refuses every place off Linux with the kind host. 13047a51 does
// this on macOS and Windows for a missing path, a plain file, a folder, a
// relative path (Windows) and srv/<version>/server (measured).
func examinePlace(path, _ string) *placeDenial {
	return &placeDenial{kind: "host", path: path, text: hostText}
}
