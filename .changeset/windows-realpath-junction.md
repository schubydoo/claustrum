---
default: patch
---

On Windows, `files.realpath` now follows a junction to a drive path, both for the junction itself and for a name below it.
