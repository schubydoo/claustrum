---
default: patch
---

On Linux, as measured, git.worktree_create in the state fallback-git without shadowDirOff checks out the commit of its new branch and runs the settle step. With worktreeRoot its pair before the add names worktreePath as the work tree. In the state shadow it then runs the location calls again after the add and after the checkout.
