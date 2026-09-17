---
default: patch
---

On macOS the host cleaner now bounds every `lsof` run three ways, matching reference build `90fca6e6`: a 15-second command deadline, a 1-second wait for the output pipe to close, and a 17-second bound after which the run is given up on. Before, the run had no bound at all, so a stalled `lsof` held a whole cleaner pass open. The third bound is the one that matters for a process wedged in the kernel, which neither of the others can end. The values are read from that build rather than measured, because staging them needs an `lsof` that hangs. The cleaner also verifies a candidate daemon's listener first, before it inspects and samples the process. That is the reference's order. It keeps a candidate that is about to be refused from costing a three-second sampling window.
