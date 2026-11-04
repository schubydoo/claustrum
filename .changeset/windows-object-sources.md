---
default: minor
---

On Windows four git methods refuse an objects/info/alternates file with Windows line endings or too many object directories before their git calls on the repository. git.status also refuses more alternates shapes and GIT_ALTERNATE_OBJECT_DIRECTORIES, and git.status and git.info refuse a path behind 32 or more links.
