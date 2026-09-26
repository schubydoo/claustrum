---
default: minor
---

Every `git.*` method now checks the repository's git directory before git runs and refuses one that is not laid out as git writes it, as reference build `f6010b97` does.
