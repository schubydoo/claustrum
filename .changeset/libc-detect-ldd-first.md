---
default: patch
---

`-install` on linux now runs `ldd --version` first and reports `libc` from its output (`musl` when the banner says musl, `glibc` for any other output), consulting the `/lib/ld-musl-*.so.*` loader glob only when `ldd` produced nothing, so a glibc host that also carries a musl loader marker now reports `glibc` instead of `musl`, matching reference build 3ef9370.
