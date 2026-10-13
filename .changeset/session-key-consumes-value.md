---
default: patch
---

When `process.spawn` looks for a session to supersede, it reads the argument after a bare `--session-id` or `--resume` as its value and no longer reads it again as a flag.
