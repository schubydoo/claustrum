---
default: patch
---

On Windows, a `process.spawn` child now gets the daemon's path under the name `PATH`, as on `89cb6289`. A caller `Path` key no longer reaches it.
