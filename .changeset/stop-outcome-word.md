---
default: patch
---

`-stop` now prints one outcome word: `stopped`, `none`, `terminated`, `killed` or `survivor`. On Linux and macOS, when the socket is unreachable, it stops the live claustrum daemon that holds `daemon.lock` for that socket. It keeps the socket path after a successful connect. After a failed one it removes the socket path and `daemon.token`, unless it prints `survivor`. `-bridge` and `-serve` now win over `-stop`. Measured on Linux against reference builds `f6010b97` and `90fca6e6`, these `-stop` rules match, except the rules that `docs/PROTOCOL.md` marks as claustrum's own. On macOS, claustrum keeps its holder check (D15), so a holder that the macOS reference signals can get `survivor`.
