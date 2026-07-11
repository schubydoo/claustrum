//go:build windows

package main

import (
	"fmt"
	"net"
	"os/user"

	"github.com/Microsoft/go-winio"
)

// honorListenPipe reports the effective -listen-pipe setting. On Windows the
// named-pipe transport is meaningful, so the flag is returned unchanged.
func honorListenPipe(requested bool) bool { return requested }

// startPipeTransport creates the additional Windows named-pipe listener and
// publishes its (claustrum-chosen, client-opaque) name to rpc.pipe beside the
// socket. The listener it returns is a plain net.Listener whose Accept yields a
// net.Conn, so server.run feeds it through the exact same acceptLoop/serveConn as
// the AF_UNIX socket — no protocol change.
//
// The pipe is locked to the daemon user's SID via an owner-only DACL (the pipe
// analogue of the socket's 0600 mode); no Everyone/Authenticated-Users/anonymous
// ACE is granted, and a remote principal cannot open it. On any error the caller
// logs and keeps serving on the socket — the transport is strictly additive.
func startPipeTransport(socket string) (net.Listener, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, fmt.Errorf("resolve current user SID: %w", err)
	}
	id, err := newInstanceID()
	if err != nil {
		return nil, err
	}
	name := pipePath(id)
	// MessageMode is left false: a byte stream matches the socket's newline-framed
	// JSON, which serveConn's bufio.Scanner splits — the wire framing is identical.
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: ownerOnlySDDL(sid)})
	if err != nil {
		return nil, fmt.Errorf("listen named pipe %s: %w", name, err)
	}
	// Publish the name only after the pipe exists and before the caller starts
	// accepting; on a write failure close the listener so we never leave a pipe
	// with no discoverable name (and no rpc.pipe to clean up).
	if err := writePipeNameFile(socket, name); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("write %s: %w", pipeNameFileName, err)
	}
	return ln, nil
}

// currentUserSID returns the daemon user's Windows security identifier string
// (e.g. S-1-5-21-…), used to build the owner-only pipe DACL. On Windows os/user
// reports Uid as the account SID.
func currentUserSID() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	if u.Uid == "" {
		return "", fmt.Errorf("empty SID for current user")
	}
	return u.Uid, nil
}
