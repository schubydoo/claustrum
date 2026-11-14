package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// peerCheckEnv is the variable of the daemon's own environment that turns the
// peer check on. On 5fd08069 it sets the peerCheck member too (measured on
// Linux, macOS and Windows). The launcher hands its whole environment to the
// daemonized child (daemonizeWithToken), so the variable reaches the daemon. The
// daemon reads it and then removes it from its own environment (startPeerCheck).
const peerCheckEnv = "CLAUDE_SSH_PEER_CHECK"

// The values of the peerCheck member of server.capabilities. 5fd08069 answers
// "on" on Linux with the variable at 1, and "unavailable" on macOS and Windows
// (measured on Linux, macOS and Windows). The values "network_only" and
// "by_pid", and "unavailable" on Linux, are claustrum's own, not measured. They
// name a kernel that lacks a part of the own text.
const (
	peerCheckOff         = "off"
	peerCheckUnavailable = "unavailable"
	peerCheckOn          = "on"
	peerCheckNetworkOnly = "network_only"
	peerCheckByPID       = "by_pid"
)

// peerCheckUnavailableLine is the line of a daemon that starts with
// CLAUDE_SSH_PEER_CHECK=1 and names no namespaces. The text is the one of
// 5fd08069 on macOS and Windows, where it came directly before the listening
// line. On Linux it is the line of a kernel with no own text (claustrum's own,
// not measured).
const peerCheckUnavailableLine = "[Server] this system names no namespaces: cannot tell callers in a sandbox from others, serving all"

// peerRefusedMessage is the message of that frame (measured on Linux).
const peerRefusedMessage = "Refused: this server could not confirm that the caller is in its own namespaces"

// The reasons of a refusal (measured on Linux).
const (
	peerReasonOther      = "other_namespace"
	peerReasonUnreadable = "unreadable"
	peerReasonLookup     = "lookup_failed"
	peerReasonResources  = "out_of_resources"
)

// ownNamingError is a failed read of the daemon's own namespaces. Its text is the
// one of 4c111e86 (measured on Linux).
type ownNamingError struct{ err error }

func (e ownNamingError) Error() string {
	return "cannot name this server's own namespaces: " + e.err.Error()
}

func (e ownNamingError) Unwrap() error { return e.err }

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
// That is the measured text of 5fd08069 (measured on Linux). A text with no user
// part shows no pidfd part: 4c111e86 logs "net:<cookie>" alone there (measured on
// Linux, with pidfd on).
func (t nsText) String() string {
	var parts []string
	if t.user != "" {
		parts = append(parts, t.user)
	}
	if t.net != "" {
		parts = append(parts, t.net)
	}
	if t.pidfd && t.user != "" {
		parts = append(parts, "pidfd")
	}
	return strings.Join(parts, " ")
}

// names tells if the text names a namespace. A pidfd part alone names none
// (claustrum's own, not measured).
func (t nsText) names() bool { return t.user != "" || t.net != "" }

// state is the peerCheck member of a daemon with this own text. All three parts
// give "on" (measured on Linux). The other states are claustrum's own, not
// measured.
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

// by is the peerCheckBy member. All parts give ["network","pidfd"]
// (measured). The value for fewer parts is claustrum's own, not measured.
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
	// mu guards own and namingErr. A failed start naming is tried again at each
	// connection, and a read that works sets both.
	mu  sync.Mutex
	own nsText
	// namingErr is the failed naming of the own namespaces at the start. While it
	// is set, the gate refuses every caller.
	namingErr error
	// refused counts the refused callers of this daemon, from 1.
	refused atomic.Int64
}

// peerCheckAsked tells if the variable has the exact text "1". On 5fd08069 each of
// these gave "off". Linux: no variable, "0", "off", the empty text, "true", "yes",
// "on", "2", "01" and " 1". macOS: no variable, "0", "off", the empty text and
// "true". Windows: no variable, "0" and "off".
func peerCheckAsked() bool {
	return os.Getenv(peerCheckEnv) == "1"
}

// unsetenv is os.Unsetenv. A test replaces it to see the line of a failed removal.
var unsetenv = os.Unsetenv

// lookupPeerOf is lookupPeer. A test replaces it to give a caller a text.
var lookupPeerOf = lookupPeer

// readDaemonNSText is daemonNSText. A test replaces it to give a daemon
// another own text or a failed naming.
var readDaemonNSText = daemonNSText

// startPeerCheck reads the variable once, at the start of the daemon, and then
// removes it from the daemon's own environment, with any value. So no child of
// the daemon inherits it: no process.spawn child, no git call, no launcher run and
// no login-shell read. With the value 1 it reads the own text, writes one line and
// returns the gate. Otherwise it returns nil, and the daemon serves every caller.
//
// On 5fd08069 a process.spawn child had no such entry for the daemon values 1 and
// 0 (measured on Linux, macOS and Windows). The git calls of one git.info had none
// for the value 0 (measured on Linux). A marker variable of the daemon arrived in
// each of them. The start environment of that daemon process still showed the
// variable (measured on Linux and macOS). The launcher runs and the login-shell
// read of 5fd08069 are not measured. A spawn env param that names the variable
// reaches the child, as on 5fd08069 (measured on Linux, macOS and Windows).
//
// A removal that fails gets one WARN line, claustrum's own. The children of the
// daemon then inherit the variable.
//
// The launcher never calls this, so it hands the variable to the daemonized child.
//
// The levels are claustrum's own. From the code, the line comes after the lines
// of the run-dir claim and of the reap, and before the listening line. On 5fd08069
// the line came directly before the listening line on macOS and Windows. On Linux
// it came after the open-files line (measured on Linux). Its place among the lines
// of a reap or of a failed token write was not measured.
func startPeerCheck() *peerGate {
	asked := peerCheckAsked()
	if err := unsetenv(peerCheckEnv); err != nil {
		logging.Warnf("[Server] cannot remove %s from the daemon environment (%v): child processes inherit it", peerCheckEnv, err)
	}
	if !asked {
		return nil
	}
	own, err := readDaemonNSText()
	g := &peerGate{own: own, namingErr: err}
	if err != nil {
		// The text is the one of 4c111e86 (measured on Linux). The level is
		// claustrum's own.
		logging.Warnf("[Server] refusing every caller for now: %v", err)
		return g
	}
	if !g.own.names() {
		logging.Warnf("%s", peerCheckUnavailableLine)
		return g
	}
	logging.Infof("[Server] serving only callers in %q", g.own.String())
	return g
}

// peerCheckAnswer is the peerCheck member of server.capabilities.
func (s *server) peerCheckAnswer() string {
	if s.peerCheck == nil {
		return peerCheckOff
	}
	return s.peerCheck.ownText().state()
}

// peerCheckByAnswer is the peerCheckBy member of server.capabilities.
func (s *server) peerCheckByAnswer() []string {
	if s.peerCheck == nil {
		return nil
	}
	return s.peerCheck.ownText().by()
}

// errPeerExited is the error of a caller that exited before the check. The text
// is claustrum's own, not measured.
var errPeerExited = errors.New("the caller has exited")

// judgePeer gives the reason of a refusal and the detail of its log line, or the
// empty reason for a caller that the daemon serves. An ENFILE or ENOMEM at any
// step gives "out_of_resources", also for the own namespaces (4c111e86). A failed
// read of the own namespaces gives "lookup_failed" for EACCES (4c111e86). Another
// permission error gives "unreadable" (measured). An exited caller gets
// "lookup_failed" (measured). claustrum gives every other failure "lookup_failed"
// too (claustrum's own, not measured). EMFILE in the log line is measured (Linux).
// A peer text with no user part, where the own text has one, gives
// "out_of_resources" with the peer text in quotes (4c111e86). Another peer text
// that is not the own text gives "other_namespace" with the peer text in quotes
// (measured). Measured on Linux.
func judgePeer(own, peer nsText, err error) (reason, detail string) {
	var naming ownNamingError
	switch {
	case err != nil && (errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ENOMEM)):
		return peerReasonResources, err.Error()
	case err != nil && errors.As(err, &naming):
		return peerReasonLookup, err.Error()
	case err != nil && errors.Is(err, fs.ErrPermission):
		return peerReasonUnreadable, err.Error()
	case err != nil:
		return peerReasonLookup, err.Error()
	case peer != own && peer.user == "" && own.user != "":
		return peerReasonResources, fmt.Sprintf("%q", peer.String())
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

// peerRefusalLine is the refusal frame with its newline (measured on Linux).
func peerRefusalLine(reason string) []byte {
	b, _ := json.Marshal(peerRefusalFrame{
		JSONRPC: "2.0",
		Error:   peerRefusalError{Code: rpc.CodePeerRefused, Message: peerRefusedMessage, Data: peerRefusalData{Reason: reason}},
	})
	// The refusal is the first line of its connection, so "at" is 0 (build
	// 52d82f0a, measured on Linux). The check refuses on Linux only.
	return conn.StampEnd(append(b, '\n'), 0)
}

// peerRefusalLogged tells if the refusal number n gets a log line. Refusals 1 to
// 10 get a log line, and 11 and 12 do not (measured).
func peerRefusalLogged(n int64) bool {
	return n <= 10
}

// admit checks one accepted connection before the daemon reads from it. It
// returns true for a caller that the daemon serves. A refused caller gets the
// refusal frame, and then the connection closes with no read. A caller that
// wrote first then reads a reset (measured). A refusal of a caller that
// exited writes no line (measured) but counts (not measured). A gate that
// names no namespace serves every caller.
//
// After a failed start naming, each connection reads the own namespaces again
// (4c111e86, measured on Linux). A read that fails refuses the caller with that
// error, and the line shows the own text "". A read that works ends the refusals
// from then on (claustrum's own, not measured).
func (g *peerGate) admit(nc net.Conn) bool {
	if g == nil {
		return true
	}
	own, err := g.ownOrRetry()
	if err == nil && !own.names() {
		return true
	}
	var peer nsText
	exited := false
	if err == nil {
		peer, exited, err = lookupPeerOf(nc, own)
	}
	reason, detail := judgePeer(own, peer, err)
	if reason == "" {
		return true
	}
	_ = nc.SetWriteDeadline(time.Now().Add(peerRefusalWriteLimit))
	_, _ = nc.Write(peerRefusalLine(reason))
	_ = nc.Close()
	n := g.refused.Add(1)
	if !exited && peerRefusalLogged(n) {
		logging.Warnf("[Server] refused caller number %d (%s): %s; this server's namespaces are %q", n, reason, detail, own.String())
	}
	return false
}

// ownText is the own text of the gate.
func (g *peerGate) ownText() nsText {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.own
}

// ownOrRetry gives the own text of the gate. After a failed start naming it
// reads the own namespaces again, and keeps a read that works.
func (g *peerGate) ownOrRetry() (nsText, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.namingErr == nil {
		return g.own, nil
	}
	own, err := readDaemonNSText()
	if err != nil {
		return nsText{}, err
	}
	g.own, g.namingErr = own, nil
	return own, nil
}
