---
default: minor
---

`git.info` now takes its root from repository discovery, also through a junction on Windows, and `server.capabilities` lists `git.info.discovered_root`. `git.status` reports worktree and submodule entries as the reference does, and refuses index files over 2 GiB in total on a linked worktree.
