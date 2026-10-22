---
default: patch
---

In the state `shadow` the git call about the repository now drops the daemon's `GIT_NAMESPACE`, `GIT_INDEX_FILE`, `GIT_DISCOVERY_ACROSS_FILESYSTEM` and `GIT_ALTERNATE_OBJECT_DIRECTORIES`. It keeps the daemon's `GIT_CONFIG_COUNT` pairs.
