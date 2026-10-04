---
default: patch
---

On Windows, a `process.spawn` child now gets the daemon's own path as `PATH=`, as on `89cb6289`.
