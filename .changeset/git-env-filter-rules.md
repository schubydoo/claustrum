---
default: minor
---

The environment filter of the git calls drops an entry with no `=`, the pathspec variables, relative home and certificate paths, relative Kerberos files and relative program names, keeps more list variables absolute, and counts a UNC path as absolute on Windows. A `~` include path of the repository config now takes `HOME` on Linux and macOS.
