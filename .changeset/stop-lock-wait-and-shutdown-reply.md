---
default: patch
---

On Linux and macOS `-stop` now waits up to 3 s for `daemon.lock` after its request and then signals a daemon that still holds it, as `5fd08069` does. The `server.shutdown` reply no longer waits for the close of the connections, so it can reach the client.
