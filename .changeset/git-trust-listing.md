---
default: minor
---

`git.info` takes its root from repository discovery, and `server.capabilities` advertises `git.info.discovered_root`. The git methods pin each hook name of the configuration listing and trust a stray `commondir` of `.` or `./`. They answer a failed listing by its cause, as `89cb6289` does.
