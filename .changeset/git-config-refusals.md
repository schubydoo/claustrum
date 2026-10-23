---
default: minor
---

The git methods now refuse a repository config that the daemon does not accept, such as a format version other than 0 or 1, a config file over 8 MiB, or a broken `config.worktree` with `extensions.worktreeConfig` on. `"shadowDirOff":true` turns this off.
