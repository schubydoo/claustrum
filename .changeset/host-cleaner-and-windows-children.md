---
default: minor
---

The host cleaner no longer ends stranded daemons. On Windows the daemon starts outside the job of its SSH session, a kill ends only the direct child, and `-keep-children` works. If descendants hold the pipes of a killed child, `process.killAndWait` answers `escalated:true` after 5 s. The shutdown and cleaner lines carry the measured `89cb6289` texts.
