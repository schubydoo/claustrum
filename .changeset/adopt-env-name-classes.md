---
default: patch
---

`server.adoptEnv` gives the session-bound names only to the sending connection, keeps the daemon temp folders and the `kept` names, and counts each name once. It tests a NUL before a missing `=`, and each entry before the count.
