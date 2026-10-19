---
default: patch
---

The request that makes the folder of the daemon in `git-shadow` now removes the folders of daemons that are gone, as `5fd08069` does. A folder goes only if its lock can be taken. Its name must also have this host form, or the folder must be old: 8 days with a lock file goes and 6 days stays. The folder of the daemon now keeps 128 repository folders, and a new repository removes one of them.
