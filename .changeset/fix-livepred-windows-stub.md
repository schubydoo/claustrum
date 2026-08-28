---
default: patch
---

On Windows, skip the dial-based daemon predecessor probe: `livePredecessorIdent` returns nil (OS-split into `livepred_unix.go` / `livepred_windows.go`), since the probe has no observable effect there — a live-predecessor second launch is the same ~0.01s either way, matching the reference — with detection kept on unix only.
