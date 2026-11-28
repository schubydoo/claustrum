---
default: minor
---

git.worktree_create now answers repository_of_another_account for a repository of another account on Linux and macOS, as 13047a51 does. On Windows, where the settings copy runs, a repository that git refuses for its owner gets the unusable-git text, as 87375cc7 and 13047a51 give.
