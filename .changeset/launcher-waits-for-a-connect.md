---
default: patch
---

A daemon take-over now follows the reference. The `-serve` launcher waits up to 12 s and exits 0 only after a connect works. On Linux and macOS an old daemon no longer removes the new daemon's socket, and on Windows it no longer removes the new `daemon.token`.
