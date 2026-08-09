---
default: minor
---

`-install` no longer bounds the `-cli-url` download at 5 minutes by default, so a slow-but-honest download the reference completes now succeeds rather than failing with `cliError "download failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)"`; opt-in via `-cli-download-timeout` or the `cli-download-timeout` key in `claustrum.conf`.
