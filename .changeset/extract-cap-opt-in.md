---
default: minor
---

`files.extract_tar` no longer caps extraction at 512 MiB by default, so a large tree extracts instead of failing with an error the reference daemon never produces; the cap is now opt-in via `-max-extract-bytes` or the `max-extract-bytes` key in `claustrum.conf`.
