---
default: minor
---

On Linux and macOS a `-serve` daemon now holds an exclusive lock on `daemon.lock` in its socket directory. At startup it evicts a prior live daemon of the same socket with SIGTERM then SIGKILL before taking over. This matches reference build 4534d86. On macOS claustrum also verifies the lock holder is our serve process before it signals it, a hardening divergence recorded as D15. Windows keeps the socket rebind handoff and gains no run-dir lock.
