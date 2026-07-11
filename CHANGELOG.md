# Changelog

All notable changes to claustrum are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and this project adheres to
[Semantic Versioning](https://semver.org/).

## [1.7.0](https://github.com/schubydoo/claustrum/compare/v1.6.0...v1.7.0) (2026-07-11)


### Features

* add opt-in Windows named-pipe transport (-listen-pipe) ([#134](https://github.com/schubydoo/claustrum/issues/134)) ([606fb2b](https://github.com/schubydoo/claustrum/commit/606fb2b5265ca85c345b3b2970430ce9201c6221))

## [1.6.0](https://github.com/schubydoo/claustrum/compare/v1.5.0...v1.6.0) (2026-07-11)


### Features

* persist auth token to daemon.token for client reconnect (ref 5db5e4a) ([#131](https://github.com/schubydoo/claustrum/issues/131)) ([3007358](https://github.com/schubydoo/claustrum/commit/3007358308ec015c5eec2ce950ca75814993af9b))

## [1.5.0](https://github.com/schubydoo/claustrum/compare/v1.4.0...v1.5.0) (2026-07-03)


### Features

* opt-in claustrum.conf config file for divergences (CT-3) ([#128](https://github.com/schubydoo/claustrum/issues/128)) ([f9b4743](https://github.com/schubydoo/claustrum/commit/f9b474327e5dfdcb89d80ed495e3e0c0a00c251b))

## [1.4.0](https://github.com/schubydoo/claustrum/compare/v1.3.1...v1.4.0) (2026-07-03)


### Features

* track reference 7c2f88d — killAndWait, stdin offset, git.info repoSlug/defaultBranch ([#120](https://github.com/schubydoo/claustrum/issues/120)) ([3cd5521](https://github.com/schubydoo/claustrum/commit/3cd5521612b6fa94e50a1d85c06bd42a14c4b6f6))

## [1.3.1](https://github.com/schubydoo/claustrum/compare/v1.3.0...v1.3.1) (2026-06-12)


### Bug Fixes

* namespace the daemonize re-exec sentinel to dodge CLAUDE_SSH_DAEMON_CHILD collision ([#111](https://github.com/schubydoo/claustrum/issues/111)) ([0e60c9c](https://github.com/schubydoo/claustrum/commit/0e60c9cd5856be1f44d4c38b86daf9526cfc651f))

## [1.3.0](https://github.com/schubydoo/claustrum/compare/v1.2.0...v1.3.0) (2026-06-12)


### Features

* opt-in -keep-children serve flag to survive daemon restart (CT-2) ([#108](https://github.com/schubydoo/claustrum/issues/108)) ([5da6f3f](https://github.com/schubydoo/claustrum/commit/5da6f3faf8326fbec21634bd6085efbd613ad50b))
* opt-in wantPid (pid + startTime) on process.spawn/reattach (CT-1) ([#105](https://github.com/schubydoo/claustrum/issues/105)) ([ae8e0d6](https://github.com/schubydoo/claustrum/commit/ae8e0d680f11995bb331e122c4777fe6a68cd1ba))

## [1.2.0](https://github.com/schubydoo/claustrum/compare/v1.1.0...v1.2.0) (2026-06-12)


### Features

* enforce "result types are ordered structs, not maps" via ast-grep ([#104](https://github.com/schubydoo/claustrum/issues/104)) ([f5512d7](https://github.com/schubydoo/claustrum/commit/f5512d7eda424c4d02a5ba345e6516418cbd1ba7))


### Bug Fixes

* filter hidden entries from files.list to match the reference ([#98](https://github.com/schubydoo/claustrum/issues/98)) ([23d2732](https://github.com/schubydoo/claustrum/commit/23d27325c413fbe5e3ae0437ad45af7cc42de250))

## [1.1.0](https://github.com/schubydoo/claustrum/compare/v1.0.1...v1.1.0) (2026-06-10)


### Features

* -token-fd to pass the auth token via a descriptor (IMPROVEMENTS [#18](https://github.com/schubydoo/claustrum/issues/18)) ([#80](https://github.com/schubydoo/claustrum/issues/80)) ([37c81ce](https://github.com/schubydoo/claustrum/commit/37c81ce0ab2190ccfde41eb9ecdbf5420873c2b3))
* async process.stdin with bounded backpressure (IMPROVEMENTS [#9](https://github.com/schubydoo/claustrum/issues/9)) ([#69](https://github.com/schubydoo/claustrum/issues/69)) ([d5434d0](https://github.com/schubydoo/claustrum/commit/d5434d04313c4bb9fb140128ab5665d17ce8afc5))
* kill orphaned process on duplicate-id spawn (IMPROVEMENTS [#17](https://github.com/schubydoo/claustrum/issues/17)) ([#79](https://github.com/schubydoo/claustrum/issues/79)) ([b4493b0](https://github.com/schubydoo/claustrum/commit/b4493b06d96a35973bdcacc79972f07a8a5a8d6b))
* opt-in Prometheus metrics endpoint (IMPROVEMENTS [#16](https://github.com/schubydoo/claustrum/issues/16)) ([#78](https://github.com/schubydoo/claustrum/issues/78)) ([3101063](https://github.com/schubydoo/claustrum/commit/31010634e3f54b37ba5aa3cdbe03aac3bf53e37b))
* tiny leveled logger preserving log prefixes (IMPROVEMENTS [#13](https://github.com/schubydoo/claustrum/issues/13)) ([#72](https://github.com/schubydoo/claustrum/issues/72)) ([3955980](https://github.com/schubydoo/claustrum/commit/395598025efc253dc3c7ef5e30ea958392405f64))
* verify -cli-checksum on the -cli-zst path when supplied (D1) ([#65](https://github.com/schubydoo/claustrum/issues/65)) ([72de5a7](https://github.com/schubydoo/claustrum/commit/72de5a7fdbb67a3e18c9347320931f6571742e39))
* Windows process-tree kill via Job Objects (IMPROVEMENTS [#14](https://github.com/schubydoo/claustrum/issues/14)) ([#73](https://github.com/schubydoo/claustrum/issues/73)) ([7a2ef9b](https://github.com/schubydoo/claustrum/commit/7a2ef9bf3a0aa16c499fd289c77c2f1b56d0a5d2))


### Bug Fixes

* add git.info root field to match reference daemon 7cbfa471 ([#60](https://github.com/schubydoo/claustrum/issues/60)) ([dcca081](https://github.com/schubydoo/claustrum/commit/dcca081c688ffc8f20a706e9ec016513930a82ee))
* add HTTP client timeout and response size cap to httpGet ([#59](https://github.com/schubydoo/claustrum/issues/59)) ([e0af84e](https://github.com/schubydoo/claustrum/commit/e0af84edef5e03ca84ef4c48d99403ec03c592b8))
* bound -install ldd libc probe with a timeout ([#63](https://github.com/schubydoo/claustrum/issues/63)) ([8083a4a](https://github.com/schubydoo/claustrum/commit/8083a4a8d6fd69265bef462b7a29cd8625faa335))
* bound git invocations with a timeout (IMPROVEMENTS [#5](https://github.com/schubydoo/claustrum/issues/5)) ([#66](https://github.com/schubydoo/claustrum/issues/66)) ([e7a0746](https://github.com/schubydoo/claustrum/commit/e7a0746ca7911546be493e931193ebd517c18d98))
* bump x/sys to v0.44.0 + Go 1.25 to clear GO-2026-5024 ([#12](https://github.com/schubydoo/claustrum/issues/12)) ([#86](https://github.com/schubydoo/claustrum/issues/86)) ([4e44305](https://github.com/schubydoo/claustrum/commit/4e44305a52709fb6b0e50aa18979f70defca238a))
* cap per-process replay buffer to prevent unbounded memory growth ([#58](https://github.com/schubydoo/claustrum/issues/58)) ([3ce3035](https://github.com/schubydoo/claustrum/commit/3ce3035236714339555ef529a1296b3ef05f6c93))
* cap zstdDecompress output size to prevent disk exhaustion ([#57](https://github.com/schubydoo/claustrum/issues/57)) ([a22b609](https://github.com/schubydoo/claustrum/commit/a22b609c51a6f9a4c1fa5181f0002f85f96adc20))
* guard files.read against non-regular files; cap extractTarGz size ([#56](https://github.com/schubydoo/claustrum/issues/56)) ([1fcb4e8](https://github.com/schubydoo/claustrum/commit/1fcb4e89c65ba50c6dcda579791f921aa14263b3))
* make -install extract atomic via temp + rename (IMPROVEMENTS [#4](https://github.com/schubydoo/claustrum/issues/4)) ([#68](https://github.com/schubydoo/claustrum/issues/68)) ([6c51cde](https://github.com/schubydoo/claustrum/commit/6c51cde30b041d82fe53afba360ff7fb6e29c13c))
* render Material icon shortcodes on the docs site ([#75](https://github.com/schubydoo/claustrum/issues/75)) ([fd1685a](https://github.com/schubydoo/claustrum/commit/fd1685aa74676d8d3bdad35ec0a84ae78687032c))
* run extractLoginPATH in goroutine; add process-group kill on timeout ([#53](https://github.com/schubydoo/claustrum/issues/53)) ([899c1a6](https://github.com/schubydoo/claustrum/commit/899c1a6ceb39a0d1bb68c1cea696fdf663ca96f2))
* skip group-kill on exited children; kill 5 lived mutants; refresh docs ([#90](https://github.com/schubydoo/claustrum/issues/90)) ([f23de46](https://github.com/schubydoo/claustrum/commit/f23de462109b5a24ab549120d8c72b3df962f3d0))
* strip CLAUDE_RPC_TOKEN from spawned child env to match reference binary ([#55](https://github.com/schubydoo/claustrum/issues/55)) ([d5f0149](https://github.com/schubydoo/claustrum/commit/d5f0149a5403d06547f161ff33f680899b70f022))
* use constant-time comparison for the RPC auth token ([#64](https://github.com/schubydoo/claustrum/issues/64)) ([a7bce19](https://github.com/schubydoo/claustrum/commit/a7bce1923de1004aada53cab66507705b56596b5))


### Performance

* marshal stream frames once per emit, not once per subscriber ([#83](https://github.com/schubydoo/claustrum/issues/83)) ([de2ed22](https://github.com/schubydoo/claustrum/commit/de2ed2285b0c33f46aa09bf29bfbfd74e43059d1))


### Build System & Dependencies

* add pre-commit git hook mirroring CI lint (IMPROVEMENTS [#6](https://github.com/schubydoo/claustrum/issues/6)) ([#71](https://github.com/schubydoo/claustrum/issues/71)) ([f6223cc](https://github.com/schubydoo/claustrum/commit/f6223cc815cac3484a8a4f615a57d983059480e0))
* pin the Go toolchain (IMPROVEMENTS [#12](https://github.com/schubydoo/claustrum/issues/12)) ([#67](https://github.com/schubydoo/claustrum/issues/67)) ([fbf4d75](https://github.com/schubydoo/claustrum/commit/fbf4d75f028a122f720c06d6fa6f772af9e943e6))

## [1.0.1](https://github.com/schubydoo/claustrum/compare/v1.0.0...v1.0.1) (2026-06-08)


### Bug Fixes

* **ci:** reference SLSA generator by semver tag, not SHA ([#44](https://github.com/schubydoo/claustrum/issues/44)) ([e31b52f](https://github.com/schubydoo/claustrum/commit/e31b52fc7ff75df76f1256090fd1056a911b344f))

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
