---
default: patch
---

On Windows a git.status whose baseRepo is a share junction reads no excludes while none are stored, and git.info reads them, as the reference does.
