package main

import (
	"fmt"
	"io"
	"net"
)

// bridgeListenerEnv is the variable of the bridge that asks it to check the
// listener of the socket (13047a51, measured on Linux, macOS and Windows). The
// check comes before the bridge sends anything. A refused listener gets nothing
// (measured on macOS, claustrum's own on Linux and Windows).
const bridgeListenerEnv = "CLAUDE_SSH_LISTENER_CHECK"

// ownerRejectStatus is the exit status of a bridge that refused the listener
// (13047a51, measured on Linux, macOS and Windows). Any other error exits 1.
const ownerRejectStatus = 79

// The answers of the listener check. Linux answers "on" for a listener in the own
// namespaces, and macOS and Windows answer "unavailable" (13047a51, measured).
const (
	listenerAnswerOn          = "on"
	listenerAnswerUnavailable = "unavailable"
)

// The modes of the check.
const (
	ownerTestNone = iota
	ownerTestLenient
	ownerTestFirm
)

// ownerTestLevel reads the value of bridgeListenerEnv. No variable, 0 and an
// empty value turn the check off. 1 passes the answers "on" and "unavailable". 2,
// strict, yes and on pass only the answer "on" (13047a51, measured on Linux,
// macOS and Windows). On Linux both refuse a listener in another user or net
// namespace. On Linux, off, false, no, true, 3, " 1" and 00 are strict too
// (13047a51, measured).
func ownerTestLevel(value string, set bool) int {
	switch {
	case !set, value == "", value == "0":
		return ownerTestNone
	case value == "1":
		return ownerTestLenient
	}
	return ownerTestFirm
}

// ownerRejection is the error of a bridge that refused the listener. main prints
// it after "claustrum: " and exits ownerRejectStatus.
type ownerRejection struct {
	kind   string
	detail string
}

func (e *ownerRejection) Error() string {
	return fmt.Sprintf("refusing the listener (%s): %s; nothing was sent to it", e.kind, e.detail)
}

// listenerAnswerOf is the check of the system. A test replaces it.
var listenerAnswerOf = listenerAnswer

// judgeSocketOwner runs the listener check on nc, a connection of the
// bridge, before the bridge sends anything (measured on macOS). A pass writes the line
// "claustrum: listener check (<answer>)" to the writer stderr, which is io.Discard
// on a redial. The text of the reference
// differs only in the program name (13047a51, measured on Linux, macOS and
// Windows). A strict mode refuses any answer but "on" with the kind too_weak.
// The check runs after a good dial, so a failed dial still gives "dial server:"
// and exit 1 (measured on Windows).
func judgeSocketOwner(nc net.Conn, value string, set bool, stderr io.Writer) error {
	mode := ownerTestLevel(value, set)
	if mode == ownerTestNone {
		return nil
	}
	answer, refusal := listenerAnswerOf(nc)
	if refusal != nil {
		return refusal
	}
	if mode == ownerTestFirm && answer != listenerAnswerOn {
		return &ownerRejection{kind: "too_weak", detail: fmt.Sprintf("here the check can say no more than %q", answer)}
	}
	_, _ = fmt.Fprintf(stderr, "claustrum: listener check (%s)\n", answer)
	return nil
}
