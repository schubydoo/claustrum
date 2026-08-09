package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// configFileName is an optional key=value file read from the directory that holds
// the executable. When it is absent (or unreadable, non-regular, or malformed),
// claustrum behaves as a stock replica of the reference daemon: every setting it
// carries gates an opt-in divergence that is off by default. Keys mirror the CLI
// flag names; unknown keys and invalid values are ignored, so the file is
// forward-compatible and can never make a mode fail.
//
// The file is looked up next to the binary so it travels with a deploy (the same
// directory the drop-in server lands in); the desktop client only globs
// `*/server`, so a sibling config is invisible to its prune.
const configFileName = "claustrum.conf"

// maxConfigBytes caps how much of the file is read, so a huge (or endless) file
// can never balloon memory or stall startup.
const maxConfigBytes = 64 << 10 // 64 KiB

// versionSHA matches the bare commit SHA accepted for version-override. The
// reference daemon versions itself by **git SHA-1** (40 hex, e.g.
// 7c2f88d13e5f269762dd4d463aa4eb3102214110) — that is the exact string the
// desktop client compares against — so 40 hex is the real target; 64 hex
// (SHA-256) is also accepted in case the reference ever moves to that. Anything
// else (wrong length, non-hex, an already-prefixed "claude-ssh …") is rejected.
var versionSHA = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

// config is the parsed, validated file. The zero value is exactly stock defaults,
// so a missing file and an empty file behave identically. Pointer/empty fields
// distinguish "set in the file" from "absent" for CLI-precedence resolution.
type config struct {
	// versionOverride, when non-empty, is a validated lower-cased commit SHA
	// (40-hex git SHA-1, or 64-hex).
	// -version then prints "claude-ssh <sha> (via Claustrum <ver>, built <t>)"
	// so the desktop client's deploy-cache check (which matches
	// /claude-ssh\s+(\S+)/ against the SHA it pins) treats an already-deployed
	// claustrum as up-to-date and stops re-uploading the reference over it.
	versionOverride string
	// keepChildren mirrors -keep-children; nil means "not set in the file".
	keepChildren *bool
	// listenPipe mirrors -listen-pipe (Windows-only opt-in); nil means "not set in
	// the file".
	listenPipe *bool
	// metricsAddr mirrors -metrics-addr; "" means "not set in the file".
	metricsAddr string
	// maxExtractBytes mirrors -max-extract-bytes; nil means "not set in the file".
	// This is the key that matters most in practice: the cap applies to
	// files.extract_tar, whose caller is Claude Desktop, which owns the argv — so
	// a flag alone would be unreachable for the people who need it.
	maxExtractBytes *int64
	// maxCLIBytes mirrors -max-cli-bytes; nil means "not set in the file". The
	// config key matters more than the flag for the same reason: Claude Desktop
	// owns the argv on the -install invocation too.
	maxCLIBytes *int64
	// cliProbeTimeout mirrors -cli-probe-timeout; nil means "not set in the file".
	// Same reachability argument as maxCLIBytes: it applies on -install, whose
	// argv Claude Desktop owns.
	cliProbeTimeout *time.Duration
	// cliDownloadTimeout mirrors -cli-download-timeout; nil means "not set in the
	// file". Same reachability argument as maxCLIBytes: it applies on -install,
	// whose argv Claude Desktop owns.
	cliDownloadTimeout *time.Duration
	// gitTimeout mirrors -git-timeout; nil means "not set in the file". This one
	// applies to -serve rather than -install, but the reachability argument is the
	// same: Claude Desktop owns that argv too, so the config key is how an operator
	// actually opts in.
	gitTimeout *time.Duration
	// libcProbeTimeout mirrors -libc-probe-timeout; nil means "not set in the file".
	// Same reachability argument as the rest of the -install knobs: Desktop owns
	// that argv. ⚠️ NOT the same knob as cliProbeTimeout — that one bounds the
	// `<cli> --version` runnability probe (D11); this one bounds `ldd --version`
	// (D14). The names are one letter apart and the types identical, which is
	// exactly the crossing TestInstallArmWiresEachFlagToItsOwnGlobal exists to catch.
	libcProbeTimeout *time.Duration
	// filesReadRegularOnly mirrors -files-read-regular-only; nil means "not set in
	// the file". A bool rather than a threshold: the guard it gates is a predicate
	// on the file's mode, so there is no value to tune — only on or off. Same
	// reachability argument as the rest, on the -serve argv.
	filesReadRegularOnly *bool
}

// loadConfig reads and validates claustrum.conf next to the executable. It never
// fails: any problem (no executable path, missing/non-regular/unreadable file,
// malformed lines) yields a zero config, i.e. stock behavior.
func loadConfig() config {
	dir, ok := executableDir()
	if !ok {
		return config{}
	}
	return loadConfigFrom(dir)
}

// loadConfigFrom reads configFileName out of dir with the same fail-safe rules as
// loadConfig. Split out so the file-IO path is testable against a temp directory
// (loadConfig itself keys off os.Executable).
func loadConfigFrom(dir string) config {
	var cfg config
	path := filepath.Join(dir, configFileName)
	// Lstat + regular-file check rejects symlinks/FIFOs/devices/directories,
	// so a FIFO at the path can never block startup on Open/Read.
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return cfg
	}
	f, err := os.Open(path)
	if err != nil {
		return cfg
	}
	defer f.Close()
	parseConfig(io.LimitReader(f, maxConfigBytes), &cfg)
	return cfg
}

// parseConfig reads key=value lines into cfg. Blank lines and lines whose first
// non-space character is '#' are comments. Unknown keys and values that fail
// their per-key validation are ignored (the default stands).
func parseConfig(r io.Reader, cfg *config) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		applyConfigKey(cfg, strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v))
	}
}

// applyConfigKey validates and stores a single setting. Invalid input is a no-op.
func applyConfigKey(cfg *config, key, val string) {
	switch key {
	case "version-override":
		if versionSHA.MatchString(val) {
			cfg.versionOverride = strings.ToLower(val)
		}
	case "keep-children":
		if b, ok := parseConfigBool(val); ok {
			cfg.keepChildren = &b
		}
	case "listen-pipe":
		if b, ok := parseConfigBool(val); ok {
			cfg.listenPipe = &b
		}
	case "metrics-addr":
		// A host:port for the opt-in listener. Keep it printable so a stray byte
		// can't reach a log line or listen call; net.Listen validates the rest.
		if val != "" && isPrintableASCII(val) {
			cfg.metricsAddr = val
		}
	// ⚠️ These two cases are deliberately written out in full rather than sharing
	// a body. They arrived on separate branches whose conflict hunks share the
	// parse line as context, so the tempting union — an empty `case
	// "max-extract-bytes":` stacked above `case "max-cli-bytes":` — reads like a
	// fallthrough and is not one. Go would make the first key a silent no-op and
	// let the second set both caps, and gofmt / vet / lint / the suite all pass.
	case "max-extract-bytes":
		// A plain byte count; 0 disables the cap (the default). Negative values
		// and anything unparseable are rejected, so a typo can never silently
		// enable a cap the operator did not ask for.
		if n, err := strconv.ParseInt(val, 10, 64); err == nil && n >= 0 {
			cfg.maxExtractBytes = &n
		}
	case "max-cli-bytes":
		// Same shape as max-extract-bytes above, same reasoning.
		if n, err := strconv.ParseInt(val, 10, 64); err == nil && n >= 0 {
			cfg.maxCLIBytes = &n
		}
	case "cli-probe-timeout":
		// A Go duration ("20s", "2m"); 0 disables the deadline (the default).
		// Negative and unparseable values are rejected, so a typo can never
		// silently impose a deadline the reference does not have. A bare number is
		// unparseable on purpose — "15" meaning 15ns would be a trap — EXCEPT for
		// zero, of which there are unboundedly many spellings: "0"/"+0"/"-0" via
		// ParseDuration's special case, plus every zero-valued duration carrying a
		// unit ("0s", "-0m", "0h0m0s", "-0.0s"), plus "-0.4ns", a negative that
		// truncates to zero. All reach d == 0 and pass the guard below. Harmless —
		// zero IS the disabled value, so nothing here can switch the deadline on —
		// but "a bare number is always rejected" and "a negative is always
		// dropped" are both false at the edges. Do not special-case "-0s".
		if d, err := time.ParseDuration(val); err == nil && d >= 0 {
			cfg.cliProbeTimeout = &d
		}
	case "cli-download-timeout":
		// A Go duration ("10m", "90s"); 0 disables the bound (the default).
		// Negative and unparseable values are rejected, so a typo can never
		// silently impose a deadline the reference does not have. A bare number is
		// unparseable on purpose — "300" meaning 300ns would be a trap — EXCEPT for
		// zero, of which there are unboundedly many spellings ("0"/"+0"/"-0" via
		// ParseDuration's special case, every zero-valued duration with a unit, and
		// a negative that truncates like "-0.4ns"). All reach d == 0 and pass the
		// guard, which is harmless: zero IS the disabled value, so nothing accepted
		// here can switch the bound ON.
		if d, err := time.ParseDuration(val); err == nil && d >= 0 {
			cfg.cliDownloadTimeout = &d
		}
	case "git-timeout":
		// A Go duration ("60s", "2m"); 0 disables the deadline (the default).
		// Negative and unparseable values are rejected on the same reasoning as the
		// two -install durations above, including the zero-spelling edge cases: all
		// of them reach d == 0, which IS the disabled value, so nothing accepted here
		// can switch the deadline on by accident.
		if d, err := time.ParseDuration(val); err == nil && d >= 0 {
			cfg.gitTimeout = &d
		}
	case "libc-probe-timeout":
		// A Go duration ("5s", "1m"); 0 disables the deadline (the default). Same
		// parsing and the same zero/negative edges as cli-probe-timeout above, and
		// the same consequence: every accepted oddity leaves the deadline off, so
		// none of them can switch it on.
		if d, err := time.ParseDuration(val); err == nil && d >= 0 {
			cfg.libcProbeTimeout = &d
		}
	case "files-read-regular-only":
		// A bool, not a threshold — false disables the guard (the default) and is
		// the parity position. parseConfigBool rejects anything it does not
		// recognise, which leaves the key UNSET, so the flag value stands.
		//
		// ⚠️ That is NOT the same as "a typo leaves the guard off", which this
		// comment used to say and docs/PROTOCOL.md now explicitly retracts: with
		// -files-read-regular-only on the argv AND files-read-regular-only = maybe
		// in the file, cliSet is true, the config side is nil, and the guard ends up
		// ON. The exact claim — the one that holds unconditionally — is that no
		// accepted ODDITY switches the divergence on: `= true` arms it deliberately,
		// which is the whole point of the key, and nothing else this parser accepts
		// arms it at all.
		if b, ok := parseConfigBool(val); ok {
			cfg.filesReadRegularOnly = &b
		}
	}
	// Unknown keys are intentionally ignored (forward-compatibility).
}

// parseConfigBool accepts the common truthy/falsey spellings; anything else is
// rejected (ok=false), which leaves the key unset — so the caller's existing
// value stands, which is the default only when nothing else set it. (It used to
// say "a typo leaves the default in place". Same imprecision the D4 case comment
// above retracts, one level down: with a flag also passed, what stands is the
// flag. Shared by every bool key, so the wording has to hold for all of them.)
func parseConfigBool(s string) (value, ok bool) {
	switch strings.ToLower(s) {
	case "true", "1", "yes", "on":
		return true, true
	case "false", "0", "no", "off":
		return false, true
	}
	return false, false
}

// effectiveKeepChildren applies CLI-over-config-over-default precedence: an
// explicit -keep-children flag always wins; otherwise the file value (if any)
// stands; otherwise the built-in default.
func (cfg config) effectiveKeepChildren(cliVal, cliSet bool) bool {
	if !cliSet && cfg.keepChildren != nil {
		return *cfg.keepChildren
	}
	return cliVal
}

// effectiveListenPipe applies the same CLI-over-config-over-default precedence for
// -listen-pipe.
func (cfg config) effectiveListenPipe(cliVal, cliSet bool) bool {
	if !cliSet && cfg.listenPipe != nil {
		return *cfg.listenPipe
	}
	return cliVal
}

// effectiveMetricsAddr applies the same precedence for -metrics-addr.
func (cfg config) effectiveMetricsAddr(cliVal string, cliSet bool) string {
	if !cliSet && cfg.metricsAddr != "" {
		return cfg.metricsAddr
	}
	return cliVal
}

// effectiveNumeric applies CLI-over-config-over-default precedence for the numeric
// flags and normalises a negative CLI value to 0 (the disabled position), calling
// warnNeg to log it. Shared by the six size-cap / timeout flags so the precedence
// and the negative->0 rule live in one place: a negative reached the daemon
// unvalidated through the flag path before, disagreeing with the config path's
// rejection, and centralising it keeps the two from drifting apart again.
func effectiveNumeric[T int64 | time.Duration](cliVal T, cliSet bool, cfgVal *T, warnNeg func()) T {
	if !cliSet && cfgVal != nil {
		return *cfgVal
	}
	if cliVal < 0 {
		warnNeg()
		return 0
	}
	return cliVal
}

// effectiveMaxExtractBytes applies the same precedence for -max-extract-bytes.
// Unlike the others there is no "empty means unset" ambiguity to dodge: 0 is a
// meaningful value (cap disabled, the default), so the config side is a pointer.
func (cfg config) effectiveMaxExtractBytes(cliVal int64, cliSet bool) int64 {
	// The flag had no validation at all, so -max-extract-bytes -1 reached the
	// daemon as a negative while the config path rejected it; centralised in
	// effectiveNumeric now, so the two paths cannot disagree about a negative.
	return effectiveNumeric(cliVal, cliSet, cfg.maxExtractBytes, func() {
		logWarnf("[Server] -max-extract-bytes %d is negative; treating it as 0 (cap disabled)", cliVal)
	})
}

// effectiveMaxCLIBytes applies the same precedence for -max-cli-bytes, and the
// same negative handling — the asymmetry fixed for max-extract-bytes would
// otherwise be reintroduced here by its sibling.
func (cfg config) effectiveMaxCLIBytes(cliVal int64, cliSet bool) int64 {
	// [Install], not [Server]: this cap governs zstdDecompress and fetchToFile,
	// and effectiveMaxCLIBytes is reached only from the -install arm.
	return effectiveNumeric(cliVal, cliSet, cfg.maxCLIBytes, func() {
		logWarnf("[Install] -max-cli-bytes %d is negative; treating it as 0 (cap disabled)", cliVal)
	})
}

// effectiveCLIProbeTimeout applies the same precedence for -cli-probe-timeout,
// and the same negative handling as the two size caps: normalise to the disabled
// value rather than letting the flag and config paths disagree about a negative.
func (cfg config) effectiveCLIProbeTimeout(cliVal time.Duration, cliSet bool) time.Duration {
	// [Install], not [Server]: isRunnable is reached only from the -install arm.
	return effectiveNumeric(cliVal, cliSet, cfg.cliProbeTimeout, func() {
		logWarnf("[Install] -cli-probe-timeout %s is negative; treating it as 0 (no deadline)", cliVal)
	})
}

// effectiveGitTimeout applies the same precedence for -git-timeout, and the same
// negative handling. [Server], not [Install]: this bound governs the git.* methods,
// which only the daemon serves.
func (cfg config) effectiveGitTimeout(cliVal time.Duration, cliSet bool) time.Duration {
	return effectiveNumeric(cliVal, cliSet, cfg.gitTimeout, func() {
		logWarnf("[Server] -git-timeout %s is negative; treating it as 0 (no deadline)", cliVal)
	})
}

// effectiveLibcProbeTimeout applies the same precedence for -libc-probe-timeout,
// and the same negative handling as the other -install durations.
func (cfg config) effectiveLibcProbeTimeout(cliVal time.Duration, cliSet bool) time.Duration {
	// [Install], not [Server]: detectLibc is reached only from the -install arm.
	return effectiveNumeric(cliVal, cliSet, cfg.libcProbeTimeout, func() {
		logWarnf("[Install] -libc-probe-timeout %s is negative; treating it as 0 (no deadline)", cliVal)
	})
}

// effectiveFilesReadRegularOnly applies the same precedence for
// -files-read-regular-only. No negative-value normalisation to do here: a bool has
// only the two states, and false — the zero value, the declared flag default and
// what an unrecognised config value leaves in place — is the parity position.
func (cfg config) effectiveFilesReadRegularOnly(cliVal, cliSet bool) bool {
	if !cliSet && cfg.filesReadRegularOnly != nil {
		return *cfg.filesReadRegularOnly
	}
	return cliVal
}

// effectiveCLIDownloadTimeout applies the same precedence for
// -cli-download-timeout, and the same negative handling as the size caps.
func (cfg config) effectiveCLIDownloadTimeout(cliVal time.Duration, cliSet bool) time.Duration {
	// [Install], not [Server]: fetchToFile is reached only from the -install arm.
	return effectiveNumeric(cliVal, cliSet, cfg.cliDownloadTimeout, func() {
		logWarnf("[Install] -cli-download-timeout %s is negative; treating it as 0 (no timeout)", cliVal)
	})
}

// versionLine returns the exact -version output. With a validated override SHA it
// prints the impersonation line; otherwise the unchanged claustrum identity.
func versionLine(override string) string {
	if override != "" {
		return "claude-ssh " + override + " (via Claustrum " + Version + ", built " + BuildTime + ")"
	}
	return "claustrum " + Version + " (built " + BuildTime + ")"
}

// executableDir returns the directory containing the running executable; ok is
// false (callers fall back to defaults) if it cannot be resolved.
func executableDir() (dir string, ok bool) {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "", false
	}
	return filepath.Dir(exe), true
}

// isPrintableASCII reports whether s is entirely printable ASCII (0x20–0x7E),
// keeping control/escape bytes out of values that reach logs or listeners.
func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
