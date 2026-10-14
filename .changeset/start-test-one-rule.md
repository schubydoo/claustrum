---
default: patch
---

As on `5fd08069`, `git.info` answers `fallback-git` or `shadow` where some start test calls gave an error, and every git method answers an error for `No space left on device`, with every error line. With no git in the `PATH`, `git.info` answers `isRepo:false`.
