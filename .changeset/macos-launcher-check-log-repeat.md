---
default: patch
---

On macOS a refused `launcherCheck` of `launcher.resolve` logs no line when its reason repeats the last answer, until a resolve with no `launcherCheck` comes between.
