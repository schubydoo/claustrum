---
default: patch
---

On Linux the launcher check with no digest refuses 9p, AFS, Ceph, GFS2, Lustre, GPFS, OCFS2, VxFS and unknown file system kinds, and a disk kind with no real device while a 9p share is mounted.
