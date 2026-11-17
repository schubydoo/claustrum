---
default: patch
---

On Windows a folder with a reparse tag that is no link, such as a ProjFS placeholder, works as a plain folder in the git methods, and a path below a folder that cannot be looked into is refused.
