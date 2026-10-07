---
default: patch
---

On Linux and macOS the git methods now refuse in a folder that holds no repository when git cannot read a `GIT_CONFIG_KEY_<n>` of the daemon. `89cb6289` does the same.
