package main

import "time"

// readRealLineWait is the longest wait of the start line of the files handler for
// the shell read, from the start of that read. 87375cc7 logs the line about 40 ms
// after the banner, so a read that ends before that logs its line first (measured
// on macOS). The bound of 40 ms is claustrum's own.
const readRealLineWait = 40 * time.Millisecond
