---
default: patch
---

On Windows, `git.info` with `"shadowDirOff":true` and an 8.3 short daemon `GIT_DIR` of its repository and no daemon `GIT_COMMON_DIR` now runs its two `--git-dir=` calls at the long name.
