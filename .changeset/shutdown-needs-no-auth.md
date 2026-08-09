---
default: patch
---

`server.shutdown` is no longer authenticated, matching the reference, so `claustrum --stop --socket <sock>` works with no `CLAUDE_RPC_TOKEN`; every other method still requires auth.
