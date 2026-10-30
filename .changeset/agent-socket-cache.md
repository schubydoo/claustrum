---
default: patch
---

The SSH agent hand-off drops a path that is not a socket with no connect and reuses a live answer for 10 minutes while the socket file is unchanged.
