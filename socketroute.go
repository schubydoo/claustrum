package main

import (
	"errors"
	"fmt"
	"os"
)

// socketTooLongError is the error of a listen or a dial whose socket path has no
// name that fits the socket address of the system. The text is that of
// `be1c1ca5`, measured on Linux (107 bytes) and macOS (103 bytes). cause is the
// error of a failed dial by a shorter name (dialSiblingSocket), or nil.
type socketTooLongError struct {
	limit int
	cause error
}

func (e *socketTooLongError) Error() string {
	text := fmt.Sprintf("the socket path is longer than the %d bytes that this system allows", e.limit)
	if e.cause != nil {
		text += "; no shorter name for it worked: " + e.cause.Error()
	}
	return text
}

func (e *socketTooLongError) Unwrap() error { return e.cause }

// changeBackError is the error of a change of folder that could not return to the
// old working folder. Its text is claustrum's own. The line of `be1c1ca5` after a
// failed change back is not measured.
type changeBackError struct{ err error }

func (e *changeBackError) Error() string {
	return "change back to the working folder: " + e.err.Error()
}

func (e *changeBackError) Unwrap() error { return e.err }

// exitOnChangeBack ends a launcher whose connect could not change back to the
// working folder, with exit 1 (claustrum's own, not measured). Other errors pass.
func exitOnChangeBack(err error) {
	var back *changeBackError
	if errors.As(err, &back) {
		fmt.Fprintf(os.Stderr, "claustrum: %v\n", err)
		osExit(1)
	}
}
