package main

import "fmt"

// socketTooLongError is the error of a listen or a dial whose socket path has no
// name that fits the socket address of the system. The text is that of
// `be1c1ca5`, measured on Linux (107 bytes) and macOS (103 bytes).
type socketTooLongError struct{ limit int }

func (e *socketTooLongError) Error() string {
	return fmt.Sprintf("the socket path is longer than the %d bytes that this system allows", e.limit)
}
