---
default: patch
---

On Windows a git.status refused for a share junction reads no excludes while none are stored, and git.info then reads them, as the reference does.
