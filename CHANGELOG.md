# Changelog

All notable changes to claustrum are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and the project aims to follow
[Semantic Versioning](https://semver.org/) once it reaches 1.0.

## 1.0.0 (2026-06-08)


### Features

* clean-room claustrum daemon (18-method JSON-RPC over AF_UNIX) ([8cd3ceb](https://github.com/schubydoo/claustrum/commit/8cd3ceb8388846fe8f1cc3f30e1af834ce863b12))
* match the reference daemon's operational stderr logging ([#17](https://github.com/schubydoo/claustrum/issues/17)) ([693ca38](https://github.com/schubydoo/claustrum/commit/693ca385a6bbfafcba22de5f0ef91ea780db9c94))


### Bug Fixes

* -serve requires -token-file + defaults the socket ([#34](https://github.com/schubydoo/claustrum/issues/34)) ([a897794](https://github.com/schubydoo/claustrum/commit/a8977949c45f010c15b1423d90226e11ff4b70b9))
* default socket + best-effort -stop + -bridge dial framing ([#28](https://github.com/schubydoo/claustrum/issues/28)) ([0166c5e](https://github.com/schubydoo/claustrum/commit/0166c5e35d877f1ac652e2bd1eeffd5ea962dcd7))
* git.info branch resolution for unborn/detached HEAD ([#33](https://github.com/schubydoo/claustrum/issues/33)) ([2892446](https://github.com/schubydoo/claustrum/commit/28924466c7e4604da2c4ce50a9729b08ce837ad9))
* match reference -install checksum + error framing ([#29](https://github.com/schubydoo/claustrum/issues/29)) ([c3b9d17](https://github.com/schubydoo/claustrum/commit/c3b9d176db5a8c1ce74776c0eebaa52c8dbac118))
* match reference extract_tar side effects (destDir wipe, modes, .synced, archive consume) ([#15](https://github.com/schubydoo/claustrum/issues/15)) ([896fd5c](https://github.com/schubydoo/claustrum/commit/896fd5c93dc7cbf41113f44fd87a6083c29f39e8))
* match reference files.list symlink resolution and extract_tar entry-type rejection ([#16](https://github.com/schubydoo/claustrum/issues/16)) ([5a863d2](https://github.com/schubydoo/claustrum/commit/5a863d23c526fac196efa854d7b7a20aaff844e0))
* match reference process.stdin check order and exited-process error ([#36](https://github.com/schubydoo/claustrum/issues/36)) ([4357b34](https://github.com/schubydoo/claustrum/commit/4357b34c354684c7e052c84121cd4bbb34da088f))
* match reference request-size cap (1 MiB) and git non-repo result shapes ([#18](https://github.com/schubydoo/claustrum/issues/18)) ([ef3983f](https://github.com/schubydoo/claustrum/commit/ef3983fdbf08f9cd6340ada4f5020534d4815281))
* reject mistyped params and check auth before version ([#22](https://github.com/schubydoo/claustrum/issues/22)) ([f5dbd85](https://github.com/schubydoo/claustrum/commit/f5dbd85e30782d259ce7b2028e31a968a83abcf2))
* reject zip-slip paths in files.extract_tar (CodeQL go/zipslip) ([#14](https://github.com/schubydoo/claustrum/issues/14)) ([19adf3b](https://github.com/schubydoo/claustrum/commit/19adf3b0f3d5698eb8dc2ca015b872c5ab432ffb))
* strip trailing newline from -serve token file to match reference ([#37](https://github.com/schubydoo/claustrum/issues/37)) ([d906812](https://github.com/schubydoo/claustrum/commit/d906812f42859aaf51d0721722e948bbd6eabad6))

