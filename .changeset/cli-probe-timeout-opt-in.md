---
default: minor
---

`-install` no longer bounds the `<cli> --version` runnability probe at 15 seconds by default, so a slow-but-working CLI installs instead of failing with `cliError "installed cli at <path> is not runnable"` and having its staged binary deleted; opt-in via `-cli-probe-timeout` or the `cli-probe-timeout` key in `claustrum.conf`.
