---
default: patch
---

The host cleaner's busy debounce now samples for a 3-second window with a two-sample minimum. Before, it took ten fixed samples about 450 ms apart. It also runs on linux at all now: the linux version was a constant `false`, so the spare for an idle daemon that still shows a live connection never fired there. On linux the orphan reap also treats a `/proc` process with a `vsize` of 0 as gone. All of this matches reference build `90fca6e6`. Those values are read from that build rather than measured, because both paths sit behind slow age gates.
