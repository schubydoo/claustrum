package files

import "github.com/schubydoo/claustrum/internal/rpc"

// mkdirServed is false: Windows has no files.mkdir, and the call gets -32601
// (c9125eb8, measured on Windows).
const mkdirServed = false

// filesMkdir is never called on Windows, because Handle tests mkdirServed first.
func filesMkdir(req *rpc.Request) rpc.Response { return rpc.UnknownMethod(req) }
