---
default: patch
---

If git cannot read a daemon `GIT_CONFIG_KEY_<n>`, the git methods now refuse in a folder with no repository too. On Windows four also refuse a regular-file path, as `89cb6289` does.
