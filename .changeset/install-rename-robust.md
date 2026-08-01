---
default: patch
---

`-install` now stages the CLI at `.fetch-<random>` and sweeps stray `*.zst` blobs, so leftover files no longer take a `-cli-keep` slot. An occupied `cliPath` is cleared instead of failing the install with a rename error. A failed cli-dir creation now carries the `mkdir cli dir: ` prefix. `-cli-version` must now name a single entry inside `-cli-dir`. A version that reaches outside it, with `..` or through a symlink, can no longer delete a directory there. A version the orphan sweep claims (`.fetch-*` or `*.zst`) is refused too, because the sweep deleted it moments after the install.
