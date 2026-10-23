---
default: minor
---

The git methods now run git on the daemon's own copy of a repository's git settings, as the reference does. `git.info` reports the state in `settingsControl`, `server.capabilities` lists `git.shadow_dir`, and `"shadowDirOff":true` turns the copy off for that request and for git requests that start while it runs. A git method that cannot use a repository answers one of three new texts, and `git.info` answers `isRepo:false` with no git in `PATH`.
