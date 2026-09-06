---
default: patch
---

The daemon now rotates the previous `remote-server.log` to `remote-server.log.old` on start instead of deleting it, matching the reference `4534d86`, and still never follows a symlinked log or writes into a file it does not own.
