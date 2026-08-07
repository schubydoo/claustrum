---
default: minor
---

`-install` no longer bounds the `-cli-url` download at 5 minutes by default, so a slow-but-honest download completes instead of failing with `cliError "download failed: context deadline exceeded"` where the reference daemon completes it; the bound is now opt-in via `-cli-download-timeout` or the `cli-download-timeout` key in `claustrum.conf`.
