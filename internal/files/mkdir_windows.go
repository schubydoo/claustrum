package files

import "github.com/schubydoo/claustrum/internal/rpc"

// mkdirHandler is nil: Windows has no files.mkdir, and the call gets -32601
// (c9125eb8, measured on Windows).
var mkdirHandler func(*rpc.Request) rpc.Response
