package main

// The format texts of the connection log of the server, claustrum's own, not
// measured. A test checks the byte length of each.
const (
	fmtConnClosed = "[Server] Connection closed: %s"
	fmtTokenless  = "[Server] Connection number %d that never showed the token: %s"
	fmtUnauth     = "[Server] Unauthorized request number %d: method=%.64q"
	fmtParseError = "[Server] Parse error number %d: %v"
	fmtScannerErr = "[Server] scanner error on %s: %q"
)

// logSampled applies the sample rule to the count n of a line kind, counted from 1:
// the counts 1 to 10 and every 1000th.
func logSampled(n uint64) bool { return n <= 10 || n%1000 == 0 }
