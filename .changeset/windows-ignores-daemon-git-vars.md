---
default: patch
---

In the state shadow on Windows the git methods now run as with no daemon GIT_DIR and GIT_COMMON_DIR, as the reference does. A remove and a create then act on the repository of baseRepo.
