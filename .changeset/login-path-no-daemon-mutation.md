---
default: patch
---

The login-shell PATH is now applied only to spawned children instead of being installed into the daemon's own environment, so the daemon no longer resolves its own `git` through the user's PATH; a non-executable `$SHELL` falls back to a usable shell rather than failing extraction outright, and a missing PATH sentinel is logged with the shell's output.
