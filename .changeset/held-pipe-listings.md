---
default: patch
---

The config listings before a git call, the listing before the read-tree call of `git.worktree_create` and the `status` scan of `git.status` now wait until git's stdout closes, as on `5fd08069`. A child that holds only stderr now delays a config listing about 2 seconds after git exits, and a config read on standard input about 1 to 1.5 seconds, not until the child ends. That read now fails with the error text of git when git exits non-zero while such a child holds stderr. The listing before the read-tree call no longer fails when a child holds its output past 30 seconds after git exited 0. A git that still runs at that bound gets SIGTERM and then SIGKILL 2 seconds later on Linux and macOS, and an exit 0 after the SIGTERM still succeeds. A config read on standard input whose output a child holds past 30 seconds still ends about 31 seconds after its start.
