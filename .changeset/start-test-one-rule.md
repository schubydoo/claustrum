---
default: patch
---

The start test of the settings copy follows `5fd08069`: `git.info` answers `fallback-git` or `shadow` where some test calls gave an error, and an error for `No space left on device`. The error text has every line of git. With no git, `git.info` answers `isRepo:false`.
