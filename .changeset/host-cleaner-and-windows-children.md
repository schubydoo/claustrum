---
default: minor
---

On Windows the daemon now starts outside the job of its SSH session, a kill ends only the direct child, and `-keep-children` works. `process.killAndWait` answers `escalated:true` after 5 s when descendants hold the pipes of a killed child.
