---
default: patch
---

`files.extract_tar` follows `5fd08069`. On Linux and macOS the execute bit of the owner arrives as mode `0700`. A name that leaves `destDir` and comes back is refused, and the entry `error` texts changed. `server.capabilities` lists `files.extract_tar.execBit`.
