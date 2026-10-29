package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// peerCheckEnv is the variable of the daemon's own environment that turns the
// peer check on. On 5fd08069 it sets the peerCheck member too (Linux, macOS and
// Windows VMs). The launcher hands its whole environment to the daemonized child
// (daemonizeWithToken), so the variable reaches the daemon. The daemon reads it
// and then removes it from its own environment (startPeerCheck).
const peerCheckEnv = "CLAUDE_SSH_PEER_CHECK"

// The values of the peerCheck member of server.capabilities. 5fd08069 answers
// "on" on Linux with the variable at 1, and "unavailable" on macOS and Windows
// (round task011-01, Linux VM, and earlier macOS and Windows VMs). The values
// "network_only" and "by_pid", and "unavailable" on Linux, are claustrum's own,
// not measured. They name a kernel that lacks a part of the own text.
const (
	peerCheckOff         = "off"
	peerCheckUnavailable = "unavailable"
	peerCheckOn          = "on"
	peerCheckNetworkOnly = "network_only"
	peerCheckByPID       = "by_pid"
)

// peerCheckUnavailableLine is the line of a daemon that starts with
// CLAUDE_SSH_PEER_CHECK=1 and names no namespaces. The text is the one of
// 5fd08069 on macOS and Windows VMs, where it came directly before the listening
// line. On Linux it is the line of a kernel with no own text (claustrum's own,
// not measured).
const peerCheckUnavailableLine = "[Server] this system names no namespaces: cannot tell callers in a sandbox from others, serving all"

// peerRefusedMessage is the message of that frame (round task011-01, Linux VM).
const peerRefusedMessage = "Refused: this server could not confirm that the caller is in its own namespaces"

// The reasons of a refusal (round task011-01, cells P2, P3 and P7, Linux VM).
const (
	peerReasonOther      = "other_namespace"
	peerReasonUnreadable = "unreadable"
	peerReasonLookup     = "lookup_failed"
)

// peerRefusalWriteLimit is the write deadline of the refusal frame (claustrum's
// own, not measured).
const peerRefusalWriteLimit = time.Second

// nsText is the own text of one process: its user namespace, the cookie of its
// network namespace, and whether the kernel gives a pidfd for a peer. An empty
// part is one that the kernel did not give.
type nsText struct {
	user  string // the link text of /proc/<pid>/ns/user, as "user:[4026531837]"
	net   string // "net:<cookie>"
	pidfd bool
}

// String joins the parts with one space, as "user:[4026531837] net:1 pidfd".
// That is the measured text of 5fd08069 (round task011-01, Linux VM).
func (t nsText) String() string {
	var parts []string
	if t.user != "" {
		parts = append(parts, t.user)
	}
	if t.net != "" {
		parts = append(parts, t.net)
	}
	if t.pidfd {
		parts = append(parts, "pidfd")
	}
	return strings.Join(parts, " ")
}

// names tells if the text names a namespace. A pidfd part alone names none
// (claustrum's own, not measured).
func (t nsText) names() bool { return t.user != "" || t.net != "" }

// state is the peerCheck member of a daemon with this own text. All three parts
// give "on" (round task011-01, cell P1, Linux VM). The other states are
// claustrum's own, not measured.
func (t nsText) state() string {
	switch {
	case !t.names():
		return peerCheckUnavailable
	case t.user == "":
		return peerCheckNetworkOnly
	case !t.pidfd:
		return peerCheckByPID
	}
	return peerCheckOn
}

// by is the peerCheckBy member. All parts give ["network","pidfd"] (cell P1).
// The value for fewer parts is claustrum's own, not measured.
func (t nsText) by() []string {
	if !t.names() {
		return nil
	}
	var by []string
	if t.net != "" {
		by = append(by, "network")
	}
	if t.pidfd {
		by = append(by, "pidfd")
	}
	return by
}

// peerGate is the peer check of a daemon that started with the variable at 1.
type peerGate struct {
	own nsText
	// refused counts the refused callers of this daemon, from 1.
	refused atomic.Int64
}

// peerCheckAsked tells if the variable has the exact text "1". On 5fd08069 each of
// these gave "off". Linux VM: no variable, "0", "off", the empty text, "true",
// "yes", "on", "2", "01" and " 1". macOS VM: no variable, "0", "off", the empty
// text and "true". Windows VM: no variable, "0" and "off".
func peerCheckAsked() bool {
	return os.Getenv(peerCheckEnv) == "1"
}

// unsetenv is os.Unsetenv. A test replaces it to see the line of a failed removal.
var unsetenv = os.Unsetenv

// lookupPeerOf is lookupPeer. A test replaces it to give a caller a text.
var lookupPeerOf = lookupPeer

// readDaemonNSText is daemonNSText. A test replaces it to give a daemon
// another own text.
var readDaemonNSText = daemonNSText

// startPeerCheck reads the variable once, at the start of the daemon, and then
// removes it from the daemon's own environment, with any value. So no child of
// the daemon inherits it: no process.spawn child, no git call, no launcher run and
// no login-shell read. With the value 1 it reads the own text, writes one line and
// returns the gate. Otherwise it returns nil, and the daemon serves every caller.
//
// On 5fd08069 a process.spawn child had no such entry for the daemon values 1 and
// 0 (Linux, macOS and Windows VMs). The git calls of one git.info had none for the
// value 0 (Linux VM). A marker variable of the daemon arrived in each of them. The
// start environment of that daemon process still showed the variable (Linux and
// macOS VMs). The launcher runs and the login-shell read of 5fd08069 are not
// measured. A spawn env param that names the variable reaches the child, as on
// 5fd08069 (Linux, macOS and Windows VMs).
//
// A removal that fails gets one WARN line, claustrum's own. The children of the
// daemon then inherit the variable.
//
// The launcher never calls this, so it hands the variable to the daemonized child.
//
// The levels are claustrum's own. From the code, the line comes after the lines
// of the run-dir claim and of the reap, and before the listening line. On 5fd08069
// the line came directly before the listening line on macOS and Windows. On Linux
// it came after the open-files line (round task011-01, Linux VM). Its place among
// the lines of a reap or of a failed token write was not measured.
func startPeerCheck() *peerGate {
	asked := peerCheckAsked()
	if err := unsetenv(peerCheckEnv); err != nil {
		logWarnf("[Server] cannot remove %s from the daemon environment (%v): child processes inherit it", peerCheckEnv, err)
	}
	if !asked {
		return nil
	}
	g := &peerGate{own: readDaemonNSText()}
	if !g.own.names() {
		logWarnf("%s", peerCheckUnavailableLine)
		return g
	}
	logInfof("[Server] serving only callers in %q", g.own.String())
	return g
}

// peerCheckAnswer is the peerCheck member of server.capabilities.
func (s *server) peerCheckAnswer() string {
	if s.peerCheck == nil {
		return peerCheckOff
	}
	return s.peerCheck.own.state()
}

// peerCheckByAnswer is the peerCheckBy member of server.capabilities.
func (s *server) peerCheckByAnswer() []string {
	if s.peerCheck == nil {
		return nil
	}
	return s.peerCheck.own.by()
}

// errPeerExited is the error of a caller that exited before the check. The text
// is claustrum's own, not measured.
var errPeerExited = errors.New("the caller has exited")

// judgePeer gives the reason of a refusal and the detail of its log line, or the
// empty reason for a caller that the daemon serves. A lookup error that is a
// permission error gives "unreadable" (cell P3). An exited caller gets
// "lookup_failed" (cell P7). claustrum gives every other failure "lookup_failed"
// too (claustrum's own, not measured). A peer text that is not the own text gives
// "other_namespace" with the peer text in quotes (cell P2). The cells are of
// round task011-01, Linux VM.
func judgePeer(own, peer nsText, err error) (reason, detail string) {
	switch {
	case err != nil && errors.Is(err, fs.ErrPermission):
		return peerReasonUnreadable, err.Error()
	case err != nil:
		return peerReasonLookup, err.Error()
	case peer != own:
		return peerReasonOther, fmt.Sprintf("%q", peer.String())
	}
	return "", ""
}

// peerRefusalFrame is the frame of a refused caller, with "id":null.
type peerRefusalFrame struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      interface{}      `json:"id"`
	Error   peerRefusalError `json:"error"`
}

type peerRefusalError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    peerRefusalData `json:"data"`
}

type peerRefusalData struct {
	Reason string `json:"reason"`
}

// peerRefusalLine is the refusal frame with its newline (round task011-01, cells
// P2 to P8, Linux VM).
func peerRefusalLine(reason string) []byte {
	b, _ := json.Marshal(peerRefusalFrame{
		JSONRPC: "2.0",
		Error:   peerRefusalError{Code: codePeerRefused, Message: peerRefusedMessage, Data: peerRefusalData{Reason: reason}},
	})
	// The refusal is the first line of its connection, so "at" is 0 (build
	// 52d82f0a, measured on Linux). The check refuses on Linux only.
	return stampEnd(append(b, '\n'), 0)
}

// peerRefusalLogged tells if the refusal number n gets a log line. Refusals 1 to
// 10 get a log line, and 11 and 12 do not (cell P6).
func peerRefusalLogged(n int64) bool {
	return n <= 10
}

// admit checks one accepted connection before the daemon reads from it. It
// returns true for a caller that the daemon serves. A refused caller gets the
// refusal frame, and then the connection closes with no read. A caller that
// wrote first then reads a reset (cell P5). A refusal of a caller that exited
// writes no line (cell P7) but counts (not measured). A gate that names no
// namespace serves every caller.
func (g *peerGate) admit(nc net.Conn) bool {
	if g == nil || !g.own.names() {
		return true
	}
	peer, exited, err := lookupPeerOf(nc, g.own)
	reason, detail := judgePeer(g.own, peer, err)
	if reason == "" {
		return true
	}
	_ = nc.SetWriteDeadline(time.Now().Add(peerRefusalWriteLimit))
	_, _ = nc.Write(peerRefusalLine(reason))
	_ = nc.Close()
	n := g.refused.Add(1)
	if !exited && peerRefusalLogged(n) {
		logWarnf("[Server] refused caller number %d (%s): %s; this server's namespaces are %q", n, reason, detail, g.own.String())
	}
	return false
}
