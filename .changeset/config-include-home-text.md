---
default: patch
---

The git config read now keeps a `~/` include path as written, refuses an `includeIf` file with a bad line whatever its condition is, and `-serve` with no home folder exits 2.
