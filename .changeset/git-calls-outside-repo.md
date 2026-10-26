---
default: minor
---

The git calls outside a repository now start from the cleaned environment and run in one folder, the version run drops the `GIT_` names that point at a repository, the probe stores no unknown answer, and the excludes lookup follows `XDG_CONFIG_HOME` and the home folder.
