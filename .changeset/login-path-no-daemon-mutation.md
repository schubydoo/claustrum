---
default: patch
---

The login-shell PATH is now applied only to spawned children, not the daemon's own environment, so the daemon no longer resolves its own `git` through the user's PATH; a non-executable `$SHELL` falls back to a usable shell instead of failing extraction, and a missing PATH sentinel is logged with the shell's output.
