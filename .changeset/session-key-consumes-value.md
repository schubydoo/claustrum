---
default: patch
---

`process.spawn` reads the argument after a bare `--session-id` or `--resume` as its value only when it looks for a session to supersede, and no longer reads it again as a flag.
