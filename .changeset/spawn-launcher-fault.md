---
default: patch
---

On Linux a `process.spawn` launcher refusal with no `launcherCheck` now carries `launcherFault` in its `data`: `missing` for a missing launcher and `not_absolute` for an empty one.
