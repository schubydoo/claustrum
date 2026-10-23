---
default: minor
---

Adds the managed launcher: `launcher.resolve`, the `launcher` param of `process.spawn` with a child environment strip, and launcher runs of `-install` and `-probe-cli` with `CLAUDE_SSH_MANAGED_LAUNCHER=1`. There `-install` on Linux replaces a cached CLI file that is no program.
