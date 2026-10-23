---
default: patch
---

On Linux and macOS `-stop` now waits up to 3 s for `daemon.lock` and then signals a daemon that still holds it. The `server.shutdown` reply no longer waits for the close of the connections, so it can reach the client.
