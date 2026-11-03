package main

import "github.com/schubydoo/claustrum/internal/files"

// The names of internal/files under the names that the code of package main
// uses.

func (s *server) handleFiles(req *request) response { return files.Handle(req.wire()) }

func filesReadRealAvailable() bool { return files.ReadRealAvailable() }
