---
default: patch
---

On Linux and macOS, files.realpath of a file with a trailing "/" now answers {"exists":false}. A link to a file and a FIFO answer the same, also with missingLeaf. The reference does so on Linux (macOS: not measured).
