---
default: patch
---

`-install` now stages the CLI at `.fetch-<random>` like the reference, clears an occupied `cliPath` instead of failing the install with a rename error, reports a failed cli-dir creation with the `mkdir cli dir: ` prefix, sweeps stray `*.zst` blobs so they no longer consume a `-cli-keep` slot, and validates `-cli-version` so the clearing step can never recursively delete content outside `-cli-dir` (through `..` or through a symlink under it) and a version that collides with the orphan sweep is refused instead of being installed and then silently deleted.
