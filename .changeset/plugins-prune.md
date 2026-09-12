---
default: patch
---

claustrum now implements plugins.prune, a new plugins.* namespace method from reference build `19f30c46` that removes cached CLI plugin directories not used within minAgeDays under the run/<clientId>/ socket layout while keeping live (session-referenced) and young ones, and advertises it as the 19th server.capabilities method.
