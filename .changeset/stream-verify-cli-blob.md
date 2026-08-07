---
default: patch
---

`-install` no longer holds the whole CLI archive in memory: the download streams to a temporary file and is hashed as it arrives, and the local `-cli-zst` blob is hashed and decompressed straight from disk, so peak memory is now flat in the blob size (measured 886 MB to 10 MB on a 400 MiB download) with no change to any `cliError` or install outcome.
