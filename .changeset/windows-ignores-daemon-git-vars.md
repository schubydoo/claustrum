---
default: patch
---

On Windows the git methods now ignore a daemon GIT_COMMON_DIR and any daemon GIT_DIR unless a request with shadowDirOff runs, as the reference does. A remove and a create then act on the repository of baseRepo.
