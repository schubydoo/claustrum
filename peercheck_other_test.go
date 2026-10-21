//go:build !linux

package main

// peerNetnsFixture is unreachable on macOS and Windows: only Linux names
// namespaces, so no test there drives the "peer-netns" helper mode. The stub keeps
// the shared runHelper switch compiling.
func peerNetnsFixture() int { return 1 }
