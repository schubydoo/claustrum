---
default: patch
---

On Windows the excludes read of git.status now runs without five git variables of the daemon environment, as the reference does: GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_CONFIG_PARAMETERS, GIT_NO_REPLACE_OBJECTS, GIT_REPLACE_REF_BASE and GIT_SHALLOW_FILE.
