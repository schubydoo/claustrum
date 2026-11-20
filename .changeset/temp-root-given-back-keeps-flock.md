---
default: patch
---

On Linux and macOS, a private temp folder keeps its flock after its last child ends, while the daemon still checks it.
