---
default: patch
---

`server.adoptEnv` gives the session-bound names only to the sending connection, and keeps the daemon temp folders and the `kept` names. It counts each name once and logs its refusal reasons in the order of the reference.
