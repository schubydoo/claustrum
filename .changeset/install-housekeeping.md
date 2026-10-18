---
default: patch
---

The `-install` prune removes only old CLI version files and keeps the new CLI, as `5fd08069` does. The sweeps remove only regular files. A negative `-cli-keep` counts as 0, and a failure before the new CLI is in place keeps the `-cli-zst` blob. A download that is no zstd archive gets the decoder text. D13 and D18 are retired. A rename error has the `rename: ` prefix, and on Windows a denied rename is tried again. On Linux the launcher gate replaces a cached file that is no program.
