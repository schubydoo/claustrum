---
default: patch
---

`git.status` now reads only git's stdout, so warnings git writes to stderr (an unreadable `core.excludesFile`, for example) no longer appear as entries in `changes` and no longer make a clean repo report as dirty.
