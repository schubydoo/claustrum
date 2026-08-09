---
default: patch
---

The per-process replay buffer counts its 16 MiB cap in serialized frame bytes, not base64 payload alone, so `process.reattach`'s `firstSeq` matches the reference on small-frame workloads instead of retaining ~9% too many frames.
