package main

import "strconv"

// stampEnd returns a new line with the end member inserted before the closing
// brace of the top-level object (build 52d82f0a, measured on Linux, macOS and
// Windows). line is one marshaled object that ends in "}\n". at is the count of
// bytes already sent on the connection. len is the byte count of the line
// before the member. The input slice is never changed, because one slice goes to
// every subscriber of a stream frame.
func stampEnd(line []byte, at int64) []byte {
	body := line[:len(line)-2] // the line without "}\n"
	out := make([]byte, 0, len(line)+40)
	out = append(out, body...)
	out = append(out, `,"end":{"at":`...)
	out = strconv.AppendInt(out, at, 10)
	out = append(out, `,"len":`...)
	out = strconv.AppendInt(out, int64(len(body)), 10)
	return append(out, "}}\n"...)
}
