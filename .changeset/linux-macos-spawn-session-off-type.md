---
default: patch
---

On Linux and macOS process.spawn now refuses a sessionFoldersOff that is not a bool or null. It answers Invalid params and starts no child, as 4c111e86 does for 1, a text, 0 and an object.
