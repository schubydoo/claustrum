package sockroute

import "fmt"

// TooLongError is the error of a listen or a dial whose socket path has no
// name that fits the socket address of the system. The text is that of
// `be1c1ca5`, measured on Linux (107 bytes) and macOS (103 bytes). cause is the
// error of a failed dial by a shorter name (DialSiblingSocket), or nil.
type TooLongError struct {
	limit int
	cause error
}

func (e *TooLongError) Error() string {
	text := fmt.Sprintf("the socket path is longer than the %d bytes that this system allows", e.limit)
	if e.cause != nil {
		text += "; no shorter name for it worked: " + e.cause.Error()
	}
	return text
}

func (e *TooLongError) Unwrap() error { return e.cause }

// ChangeBackError is the error of a change of folder that could not return to the
// old working folder. Its text is claustrum's own. The line of `be1c1ca5` after a
// failed change back is not measured.
type ChangeBackError struct{ err error }

func (e *ChangeBackError) Error() string {
	return "change back to the working folder: " + e.err.Error()
}

func (e *ChangeBackError) Unwrap() error { return e.err }
