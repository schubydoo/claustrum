package supervisor

import "unicode/utf8"

// logIDMax is the most bytes of a process id that a cut log line shows whole.
const logIDMax = 8

// LogID is id as some process log lines show it. An id of more than 8 bytes
// becomes its first 8 bytes and "…": "b_control" gives "b_contro…", and an id
// of 8 bytes stays whole. c9125eb8 and 4c111e86 cut it so in these lines,
// measured on Linux, macOS and Windows:
//
//   - Process <id> started, PID=<pid>, command=<command>
//   - Starting stdout or stderr streaming for process <id>
//   - Process <id> exited with code ...
//   - Failed to start process <id>: <text>
//
// c9125eb8 and 4c111e86 cut it in "Process <id>: no connection attached for ..." too
// (measured on Linux), and 4c111e86 in "Process <id> is being ended: its private
// temp folder is lost" (measured on Linux and macOS).
//
// claustrum's own, not measured: an id with a byte above 0x7f. claustrum cuts it
// at 8 bytes too and drops a character that the cut splits. Every other log line
// shows the whole id, also the two launcher forms of the started line.
func LogID(id string) string {
	if len(id) <= logIDMax {
		return id
	}
	n := logIDMax
	for n > 0 && !utf8.RuneStart(id[n]) {
		n--
	}
	return id[:n] + "…"
}
