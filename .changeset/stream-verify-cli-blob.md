---
default: perf
---

`-install` streams the download to a temporary file and hashes it as it arrives, and hashes and decompresses the local `-cli-zst` blob straight from disk, so peak memory is flat in the blob size (measured 886 MB to 10 MB on a 400 MiB download) with no change to any `cliError` or install outcome.
