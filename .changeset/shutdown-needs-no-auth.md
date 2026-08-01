---
default: patch
---

`server.shutdown` is no longer authenticated, matching the reference, so `claustrum --stop --socket <sock>` works from a bare command line with no `CLAUDE_RPC_TOKEN` — which is how the Claude Desktop client tears the daemon down; every other method still requires auth.
