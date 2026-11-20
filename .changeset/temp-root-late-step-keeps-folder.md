---
default: patch
---

On Linux and macOS, a private temp folder step that answers after its 2 s limit now logs its line and records its folder, so the next spawn gets that folder instead of a new one.
