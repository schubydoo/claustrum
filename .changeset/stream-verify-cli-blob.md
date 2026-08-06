---
default: patch
---

`-install` no longer holds the whole CLI archive in memory: the download streams to a temporary file and is hashed as it arrives, and the local `-cli-zst` blob is hashed and decompressed straight from disk, cutting peak memory on a large download by about half with no change to any `cliError` or install outcome.
