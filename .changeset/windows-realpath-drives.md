---
default: patch
---

On Windows files.realpath now follows the reference in five cases. A path on a SUBST drive gives its real path. A path with a colon after the drive letter is refused. A path on a mapped network drive gives no_usable_name. So does a missing name behind a network link. A chain of 32 junctions gives error 1921.
