---
default: patch
---

claustrum now prefers zsh over bash when `$SHELL` is unusable and discards a login PATH whose extraction timed out, both matching the reference, so every spawned child gets the environment the reference would have given it.
