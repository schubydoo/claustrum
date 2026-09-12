package main

// ignoreSigpipe makes SIGPIPE non-fatal for the current process. -probe-cli and
// -install stream structured output a caller consumes over a pipe (the classification
// tokens and the install JSON facts, both on fd 1), where Go's default terminates the
// process on a broken-pipe write; ignoring SIGPIPE lets the write fail with EPIPE
// instead. Set in exactly these two modes, matching reference build 19f30c46. It is a
// package var so a test can observe that the right modes call it; the OS-specific body
// is ignoreSigpipeDefault.
//
// The other fd-1 writer, -version, is deliberately excluded: its single line is not
// worth ignoring SIGPIPE for. -serve writes to client sockets (fd > 2), for which the
// Go runtime already ignores SIGPIPE.
var ignoreSigpipe = ignoreSigpipeDefault
