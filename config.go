package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	}
	// Unknown keys are intentionally ignored (forward-compatibility).
}

// parseConfigBool accepts the common truthy/falsey spellings; anything else is
// rejected (ok=false) so a typo leaves the default in place.
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
