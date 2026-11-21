---
default: patch
---

On Windows the git calls now keep a GIT_SSL_CAINFO or other path variable whose value starts with two slashes, such as `\\\x`, and drop an empty GIT_ALTERNATE_OBJECT_DIRECTORIES, as the reference does.
