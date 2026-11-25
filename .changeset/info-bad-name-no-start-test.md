---
default: patch
---

On Windows, git.info on a folder whose stat fails with ERROR_INVALID_NAME now refuses before the start test, with no git call but the first excludes read.
