---
default: minor
---

claustrum now implements the `-probe-cli <path>` mode from reference build `19f30c46`, a one-shot bounded `<path> --version` runnability probe that always exits 0. When the CLI runs, it prints nothing. When the fixed 30s deadline had to kill the CLI, it prints `__CLI_HUNG__`. When the binary is missing or does not run, it prints `__CLI_BAD__`.
