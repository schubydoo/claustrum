---
default: patch
---

On Windows, recognise a refused AF_UNIX handoff dial (`WSAECONNREFUSED`, 10061) as a dead socket via an OS-split `isConnRefused`, so the launcher's stale-socket detection fires there as it already does on POSIX (`ECONNREFUSED`).
