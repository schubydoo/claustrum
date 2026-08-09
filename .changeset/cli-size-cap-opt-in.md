---
default: minor
---

`-install` no longer caps the decompressed CLI or the download body at 512 MiB by default, so a large CLI installs instead of failing with a `cliError` the reference daemon never produces; opt-in via `-max-cli-bytes` or the `max-cli-bytes` key in `claustrum.conf`.
