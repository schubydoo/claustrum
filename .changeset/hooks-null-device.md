---
default: minor
---

On Windows each git call that sets core.hooksPath passes `\\.\NUL`, and server.capabilities lists git.hooks_null_device after git.shadow_dir on every system.
