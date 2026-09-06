---
default: patch
---

process.stdin now evaluates the offset idempotency verdict before the running check, matching the reference `4534d86`: on an exited process an offset gap returns `-32003` and a wholly-duplicate write returns `success` with `duplicate:true`, instead of `-32602 Process not running`.
