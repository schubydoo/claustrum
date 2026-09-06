---
default: minor
---

`process.spawn` now supersedes a prior running process of the same stream-json CLI session (`--session-id`/`--resume`, unless `--fork-session`), terminating it via the SIGTERM-then-SIGKILL path, matching reference build 4534d86; a spawn with no session key or a different session id supersedes nothing.
