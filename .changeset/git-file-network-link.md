---
default: patch
---

On Windows without shadowDirOff the git methods now refuse a .git file that names its entry through a link to a network path, as the reference does.
