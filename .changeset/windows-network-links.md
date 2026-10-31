---
default: patch
---

On Windows a link to a mapped network drive or a \\?\UNC\ path is no folder in files.list, and files.realpath answers no_usable_name for it. files.realpath also removes a .. inside a link target by text.
