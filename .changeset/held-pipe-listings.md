---
default: patch
---

The config listings before a git call now wait until their output pipes close. So do the listing before the read-tree call of `git.worktree_create` and the three scans of `git.status`. `5fd08069` does so for the listings and the `status` scan. The four time-limited git methods also read some config files on standard input, for example in call 7 of the start test. If a child holds the pipes of such a read past 30 seconds, the read now ends about 31 seconds after its start.
