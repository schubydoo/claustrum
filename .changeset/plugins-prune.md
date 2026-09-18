---
default: minor
---

claustrum now implements plugins.prune, a new plugins.* namespace method from reference build `19f30c46`. Under the run/<clientId>/ socket layout, it removes cached CLI plugin directories not used within minAgeDays, and it keeps live (session-referenced) and young ones. It leaves the shared legacy plugins untouched for any other install whose daemon on the host still answers or cannot be ruled out as gone. It is advertised as the 19th server.capabilities method.
