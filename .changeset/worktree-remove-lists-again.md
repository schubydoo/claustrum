---
default: patch
---

Without `worktreeRoot`, for a worktree folder that exists, `git.worktree_remove` now lists the configuration once more when its first listing fails, unless `baseRepo` holds no repository, and there a failed listing answers its text after the words of the lock check (cell CaP3). It removes the worktree when that second listing passes, as `5fd08069` does (rounds `tier3-13`, `tier3-17` and `tier3-19` on macOS, Linux and Windows VMs). When the second listing fails too, the reply quotes the text of the failed listing after the words of the lock check.
