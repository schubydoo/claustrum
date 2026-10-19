---
default: patch
---

On Windows an old daemon that stops after a take-over now keeps the `daemon.token` of the new daemon, as `5fd08069` does. On Linux and macOS the stop ends the child groups before it removes its token and releases `daemon.lock`. There a new daemon tries the lock of a `--stop` holder for 2 s before it goes on.
