---
default: minor
---

`git.worktree_create` now answers `errorCode` `repository_only_filter` for a git filter that only the repository's own config defines. With the settings copy it follows the reference rules for git-lfs filters and refuses more than 64 filters. `"shadowDirOff":true` turns the refusal off.
