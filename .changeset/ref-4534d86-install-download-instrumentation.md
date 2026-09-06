---
default: minor
---

`-install` now instruments the `-cli-url` download to match reference build 4534d86: `__INSTALL_PROGRESS__` ticker lines to stdout, a `fetch` stats object appended to `__INSTALL_RESULT__`, and an always-on 60-second read-idle stall abort (`download stalled: no data for 60s after <got>/<total> bytes`), while the opt-in total-download deadline (D12) stays off by default.
