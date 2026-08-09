---
default: patch
---

`-install` stages the CLI at `.fetch-<random>` and sweeps stray `*.zst` blobs so leftovers no longer take a `-cli-keep` slot; an occupied `cliPath` is cleared instead of failing with a rename error; a failed cli-dir creation carries the `mkdir cli dir: ` prefix; `-cli-version` must name a single entry inside `-cli-dir`, so a version reaching outside via `..` or a symlink can no longer delete a directory there, and one the orphan sweep claims (`.fetch-*` or `*.zst`) is refused; the new CLI is renamed into place before the old one is cleared, so two installs in one cli-dir cannot leave it empty; if another install reclaims its staging file, it retries once instead of failing.
