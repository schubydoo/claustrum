package files

import "github.com/schubydoo/claustrum/internal/rpc"

// findHandler is nil: Windows has no files.find, and the call gets -32601
// (4c111e86, measured on Windows).
var findHandler func(*rpc.Request) rpc.Response
