---
default: patch
---

With `worktreeRoot`, the worktree methods now refuse unsafe locations and delete nothing there: the file system root, reserved names, repository folders, long link chains, and a relative or missing `baseRepo`. On Linux and macOS create also refuses a root below a folder that a user other than you or root owns, or that a group other than your own private group, or every user, can write without the sticky bit.
