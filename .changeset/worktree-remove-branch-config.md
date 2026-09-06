---
default: patch
---

git.worktree_remove now deletes the branch with a raw `update-ref --no-deref -d` instead of `git branch -D`, matching the reference `4534d86`: the branch ref and reflog are still removed, but the branch's `[branch "<name>"]` config section is left intact rather than dropped.
