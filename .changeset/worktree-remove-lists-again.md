---
default: patch
---

If its first listing of the configuration fails, `git.worktree_remove` now lists it once more, for a worktree folder that exists and with no `worktreeRoot`. `5fd08069` does the same (rounds `tier3-13`, `tier3-17` and `tier3-19` on macOS, Linux and Windows VMs). If that second listing passes, the remove goes on and removes the worktree. If it fails too, the reply quotes the text of the failed listing after the words of the lock check. Where `baseRepo` holds no repository, no second listing runs, and a failed listing gets that same form (cell CaP3). The git 2.56 text for a `.git` file that names no valid repository keeps the lock-check refusal there, after one more listing.
