---
default: minor
---

The daemon no longer bounds every `git` invocation at 60 seconds by default, so a slow-but-honest git on a large or cold repository completes instead of being SIGKILLed into a claustrum-only `-32603 "signal: killed"` (or a `git worktree remove timed out after …`) where the reference showed no such deadline at the durations probed; now opt-in via `-git-timeout` or the `git-timeout` key in `claustrum.conf`.
