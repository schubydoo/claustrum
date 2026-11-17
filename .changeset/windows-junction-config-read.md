---
default: patch
---

On Windows git.info and git.list_branches read the config of a junction path through its target, so a junction to a bare repository passes.
