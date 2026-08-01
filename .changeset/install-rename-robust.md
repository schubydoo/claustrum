---
default: patch
---

`-install` now stages the CLI at `.fetch-<random>` like the reference, clears an occupied `cliPath` instead of failing the install with a rename error, reports a failed cli-dir creation with the `mkdir cli dir: ` prefix, and sweeps stray `*.zst` blobs so they no longer consume a `-cli-keep` slot.
