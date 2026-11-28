//go:build !linux

package main

import "net"

// listenerAnswer answers "unavailable" on macOS and Windows, which name no
// namespaces (13047a51, measured on macOS and Windows).
func listenerAnswer(net.Conn) (string, *ownerRejection) {
	return listenerAnswerUnavailable, nil
}
