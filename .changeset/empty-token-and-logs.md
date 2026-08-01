---
default: patch
---

A zero-byte `-token-file` is now a fatal startup error instead of producing a daemon that listens but can never be authenticated to, and the daemon now logs a failed token-file unlink, a child stream read error, and a discarded stdin queue rather than passing over them in silence.
