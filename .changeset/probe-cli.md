---
default: minor
---

claustrum now implements the `-probe-cli <path>` mode from reference build `19f30c46`, a one-shot bounded `<path> --version` runnability probe that always exits 0 and prints nothing when the CLI runs, `__CLI_HUNG__` when the fixed 30s deadline had to kill it, or `__CLI_BAD__` when the binary is missing or does not run.
