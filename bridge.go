package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/schubydoo/claustrum/internal/runlock"
	"github.com/schubydoo/claustrum/internal/sockroute"
	"github.com/schubydoo/claustrum/internal/sysproc"
	"github.com/schubydoo/claustrum/internal/tokenfile"
)

// runBridge is a dumb stdio<->unix-socket relay. It does NOT inject auth; the
// stream it relays must already carry "auth" per request. With CLAUDE_SSH_ADOPT_ENV
// set to 1 or no-login-shell it first sends a frame of its own (bridgeAdoptFrame). This is what an SSH
// session attaches to. A dial failure is a hard error (wrapped "dial server:",
// matching the reference). -stop instead reports a failed dial as a word.
//
// At stdin EOF the bridge half-closes the socket. It keeps relaying the socket
// to stdout. docs/protocol/flags.md (-bridge) gives the exit rule.
// Measured against f6010b97 and 90fca6e6 on Linux and Windows, the reference
// bridge got stdin `{"jsonrpc":"2.0","id":1,` then EOF. It put the 76-byte
// -32700 frame on stdout and exited 0. Measured on Linux, the reference bridge
// also exited 0 with stdin still open once the daemon closed. It printed the
// frames that arrived before the close.
//
// A write to a closed stdout ends the relay with exit 0. On Linux and macOS main
// ignores SIGPIPE for -bridge, and the copy to stdout uses plain writes
// (bridgeStdout). That follows the measured outcome of build 52d82f0a: exit 0
// after a write to a closed stdout. The claustrum bridge itself was not measured.
func runBridge(socket string) error {
	nc, err := sockroute.DialUnixPath(socket, 0)
	if err != nil {
		return fmt.Errorf("dial server: %w", err)
	}
	var from io.Reader = nc
	if frame := bridgeAdoptFrame(socket, os.Environ()); frame != nil {
		if nc, from, err = sendAdoptFrame(socket, nc, frame); err != nil {
			return fmt.Errorf("dial server: %w", err)
		}
	}
	defer nc.Close()
	// Read the globals once, here, so the copy goroutines never touch them.
	in, out := os.Stdin, os.Stdout
	go func() {
		_, _ = io.Copy(nc, in)
		closeWriteOrClose(nc)
	}()
	_, _ = io.Copy(bridgeStdout(out), from)
	return nil
}

// bridgeAdoptEnv is the variable of the bridge that asks for its first frame,
// server.adoptEnv (c9125eb8, measured on Linux, macOS and Windows). The value 1
// sends the frame. The value no-login-shell sends it with "noLoginShell":true. No
// variable and yes send none (Linux), nor do true, 0 and an empty value (macOS).
// Other values send none (claustrum's own, not measured). The arg no-login-shell
// changes nothing (macOS).
const bridgeAdoptEnv = "CLAUDE_SSH_ADOPT_ENV"

// bridgeAdoptWait is how long the bridge waits for the reply to its frame. With no
// reply the bridge closed the connection after about 1 s and connected again
// (c9125eb8, measured on Linux, macOS and Windows). var so a test can shrink it.
var bridgeAdoptWait = time.Second

// bridgeAdoptFrames is how many connections carry the frame. When neither gives a
// reply, a third connection carries no frame and relays stdin (c9125eb8, measured
// on Linux, macOS and Windows).
const bridgeAdoptFrames = 2

// bridgeAdoptMaxFrame is the largest frame that the bridge sends, in bytes without
// the newline. A frame of 262144 bytes went and one of 262145 did not (c9125eb8,
// measured on Linux and macOS). Windows sent 262144 bytes and had no row above it,
// so the same limit there is claustrum's own, not measured.
const bridgeAdoptMaxFrame = 256 << 10

// adoptFrame is the first frame of the bridge, with its members in name order as
// measured.
type adoptFrame struct {
	Auth    string      `json:"auth"`
	ID      string      `json:"id"`
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  adoptParams `json:"params"`
}

// adoptParams are the params of the frame. kept follows env, and only when it has a
// name (measured on Linux and macOS). noLoginShell is the last member, and only when
// it is true (measured). The order of kept and noLoginShell in one frame is name
// order (claustrum's own, not measured).
type adoptParams struct {
	Env          []string `json:"env"`
	Kept         []string `json:"kept,omitempty"`
	NoLoginShell bool     `json:"noLoginShell,omitempty"`
}

// bridgeAdoptFrame is the line of the first frame for env, the environment of the
// bridge, or nil when the bridge sends none. env goes in its own order (measured).
// The auth member is the content of daemon.token beside the socket, and with no
// such file the bridge sends no frame (c9125eb8, measured on Linux, macOS and
// Windows). An empty file sends no frame (claustrum's own, not measured). The frame
// holds no CLAUDE_RPC_TOKEN, and json.Marshal escapes <, > and & in a value
// (measured on Linux, macOS and Windows). The bridge does not test the list as the
// daemon does: 9000 entries went (measured on Linux, macOS and Windows), and so did
// one entry of 131072 bytes (macOS). Only the size of the frame stops it
// (bridgeAdoptMaxFrame).
//
// An entry that is not valid UTF-8 leaves env, and its name goes into kept
// (measured on Linux and macOS for a value). On Windows the environment is UTF-16,
// and Go gives such a value with U+FFFD, which stays in env (measured). A name that
// is not valid UTF-8 goes into kept the same way (claustrum's own, not measured).
func bridgeAdoptFrame(socket string, env []string) []byte {
	var noLogin bool
	switch v, _ := os.LookupEnv(bridgeAdoptEnv); v {
	case "1":
	case "no-login-shell":
		noLogin = true
	default:
		return nil
	}
	env = sysproc.RemoveEnvKey(env, "CLAUDE_RPC_TOKEN")
	sent := make([]string, 0, len(env))
	var kept []string
	for _, e := range env {
		if utf8.ValidString(e) {
			sent = append(sent, e)
			continue
		}
		name, _, _ := strings.Cut(e, "=")
		kept = append(kept, name)
	}
	token, err := os.ReadFile(filepath.Join(tokenfile.Dir(socket), tokenfile.Name))
	if err != nil || len(token) == 0 {
		return nil
	}
	b, err := json.Marshal(adoptFrame{
		Auth: string(token), ID: "bridge", JSONRPC: "2.0", Method: methodAdoptEnv,
		Params: adoptParams{Env: sent, Kept: kept, NoLoginShell: noLogin},
	})
	if err != nil || len(b) > bridgeAdoptMaxFrame {
		return nil
	}
	return append(b, '\n')
}

// sendAdoptFrame writes frame on nc and reads one reply line, which the bridge
// does not relay (measured on Linux). An error reply does not stop the relay
// (measured on Linux). It returns the connection and the reader that the relay
// goes on with. With no reply in bridgeAdoptWait, or when the daemon closes the
// connection, it closes the connection and dials again at once. The second
// connection gets the frame again. The third gets no frame, and the relay goes on
// there (c9125eb8, measured on Linux, macOS and Windows). The stdin relay starts
// after the reply or on the third connection (measured on Linux). These parts are
// claustrum's own, not measured: a failed write counts as a close, a second close
// leads to the third connection too, a failed new dial ends the bridge with "dial
// server:", and the write of the frame has no deadline of its own.
func sendAdoptFrame(socket string, nc net.Conn, frame []byte) (net.Conn, io.Reader, error) {
	for try := 1; ; try++ {
		br := bufio.NewReader(nc)
		_ = nc.SetReadDeadline(time.Now().Add(bridgeAdoptWait))
		_, err := nc.Write(frame)
		if err == nil {
			_, err = br.ReadString('\n')
		}
		if err == nil {
			_ = nc.SetReadDeadline(time.Time{})
			return nc, br, nil
		}
		_ = nc.Close()
		if nc, err = sockroute.DialUnixPath(socket, 0); err != nil {
			return nil, nil, err
		}
		if try == bridgeAdoptFrames {
			return nc, nc, nil
		}
	}
}

// closeWriteOrClose ends the write side of nc so the daemon reads EOF, and
// keeps the read side open for its last replies. If the half-close fails, it
// closes nc fully. The daemon still reads EOF then, and the bridge exits on
// the failed read, which is the behavior before the half-close.
func closeWriteOrClose(nc net.Conn) {
	if cw, ok := nc.(interface{ CloseWrite() error }); ok && cw.CloseWrite() == nil {
		return
	}
	_ = nc.Close()
}

// stopReplyTimeout bounds how long -stop waits for the daemon's reply to its
// shutdown request. 2s, measured against the reference. var so tests can shrink it.
var stopReplyTimeout = 2 * time.Second

// runStop sends an unauthenticated server.shutdown RPC to a running daemon and
// prints one outcome word on stdout. ⚠️ this stops the daemon and drops its
// sessions. In every state that the stopWord constants name, it prints a word
// and main exits 0.
func runStop(socket string, stdout io.Writer) {
	_, _ = fmt.Fprintln(stdout, stopDaemon(socket))
}

// stopDaemon does the work of -stop and returns the word to print.
//
// Measured against f6010b97 and 90fca6e6 on Linux:
//
//   - If the connect succeeds, -stop sends the request and does one read with a 2 s
//     deadline. A live daemon removes its own socket when it stops.
//   - Then -stop waits for daemon.lock on Linux and macOS (runlock.StopAfterReply), and
//     prints "stopped" at once on Windows (`5fd08069`, measured). On 5fd08069 -stop
//     prints "stopped" with rc 0 both when the reply arrives and when it gets EOF
//     (measured). claustrum sends garbage, a reset and the deadline to the same step
//     (not measured on 5fd08069).
//   - If the connect fails, -stop checks daemon.lock (runlock.StopRunDirHolder). Then it
//     removes the socket path and daemon.token beside it, and prints the word
//     from the lock check.
//   - If the word is "survivor", -stop removes neither file. This is measured
//     for a holder that -stop does not signal and for a lock that outlives
//     SIGKILL. The other survivor arms follow the same rule.
//
// The unlink on a failed connect uses os.Remove. It removes a stale socket, a
// socket that refuses the connect with EACCES, a regular file and an empty
// directory. It keeps a non-empty directory and a path in a directory that
// this user cannot write. The references do the same (measured).
func stopDaemon(socket string) string {
	nc, err := sockroute.DialUnixPath(socket, 0)
	if err != nil {
		// Remove while the lock is still held, then release it (see runlock.StopRunDirHolder).
		word, release := runlock.StopRunDirHolder(socket)
		if word != runlock.StopWordSurvivor {
			_ = os.Remove(socket)
			_ = os.Remove(filepath.Join(tokenfile.Dir(socket), tokenfile.Name))
		}
		release()
		return word
	}
	defer nc.Close()
	// No auth member. The reference's -stop sends exactly this frame, and its
	// daemon does not authenticate server.shutdown. Captured off a fake listener
	// with CLAUDE_RPC_TOKEN deliberately SET, so the omission is known to be real
	// and not just an unset variable:
	//
	//	reference : {"jsonrpc":"2.0","id":1,"method":"server.shutdown"}
	//	claustrum : {"jsonrpc":"2.0","id":1,"method":"server.shutdown","auth":"…"}
	//
	// CLAUDE_RPC_TOKEN is no longer read here at all — -stop needs no credential,
	// which is what lets `server --stop --socket <sock>` work from a bare SSH
	// command line the way the Desktop client invokes it.
	_, _ = fmt.Fprint(nc, `{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}`+"\n")
	// Bound the wait for the shutdown reply. A daemon that accepts the connection
	// and then never answers — wedged, or a stale socket now owned by something
	// else — would otherwise hang -stop forever, since a bare Read has no
	// deadline. Measured at 5db5e4a: against a socket that accepts and never
	// replies, the reference returns in 2.030s over three runs. claustrum was still
	// blocked when killed at 45s.
	_ = nc.SetReadDeadline(time.Now().Add(stopReplyTimeout))
	// One read, and its result does not matter. The reply frame is never
	// relayed to stdout: -stop is a control command, not a relay.
	buf := make([]byte, 4096)
	_, _ = nc.Read(buf)
	word, release := runlock.StopAfterReply(socket)
	release()
	return word
}
