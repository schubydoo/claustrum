---
default: patch
---

On Linux and macOS `-install` makes the folders of the `-cli-dir` text as given, so `-cli-dir D/sub/..` makes `D/sub` as `5fd08069` does (round `tier3-01`, Linux and macOS VMs). If that folder step fails, the facts line has an empty `cliPath` and the mkdir error as `cliError`.
