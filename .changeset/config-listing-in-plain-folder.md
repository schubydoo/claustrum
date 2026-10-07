---
default: patch
---

The git methods now refuse in a folder with no repository when git cannot read a `GIT_CONFIG_KEY_<n>` of the daemon. On Windows four of them also refuse a regular-file path. `89cb6289` does both.
