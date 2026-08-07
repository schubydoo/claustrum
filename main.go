// Command claustrum is a clean-room reimplementation of the small Go daemon that
// drives a remote Claude Code session: a local CLI-version manager + process
// supervisor + JSON-RPC multiplexer (with a replay buffer) over an AF_UNIX socket.
//
// It was built to a behavioral contract captured by probing the reference binary
// at the wire level; no code was copied, and no decompiler output was
// transcribed into the implementation.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
)

// Version is the daemon's self-reported version (the real binary reports its vcs
// revision hash here, e.g. a 40-char git SHA). When not overridden via
// -ldflags "-X main.Version=...", both fields are auto-derived from the embedded
// build info, in this order of precedence:
//
//	              -ldflags     >  vcs.*             >  module info      >  sentinel
//	Version:      (goreleaser)    vcs.revision         Main.Version        devSentinel
//	BuildTime:    (goreleaser)    vcs.time             releaseTime         unknownTime
//	                              (local `go build`)   (`go install @v`)
//
// matching how the real binary is stamped whenever VCS metadata is present. The
// module-info column covers `go install pkg@vX.Y.Z`, which builds from the module
// cache and embeds no vcs.* settings at all; releaseTime comes from the generated
// buildstamp.go, since no build time is recoverable at runtime on that path.
var (
	Version   = devSentinel
	BuildTime = unknownTime
)

// devSentinel and unknownTime are the placeholders the two fields carry until a
// build stamp replaces them; they double as the "not yet stamped" tests in the
// fallbacks below.
const (
	devSentinel = "claustrum-dev"
	unknownTime = "unknown"
)

// resolveVersion populates Version/BuildTime from the embedded build info unless
// they were already set at build time via -ldflags.
func resolveVersion() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	applyVCSStamp(bi.Settings)
	applyModuleVersion(bi.Main.Version)
}

// applyVCSStamp fills Version/BuildTime from the embedded VCS build settings,
// unless Version was already stamped via -ldflags (i.e. it differs from the dev
// sentinel). Split out from resolveVersion so the stamp logic is testable
// without depending on the test binary's own (absent) VCS metadata.
func applyVCSStamp(settings []debug.BuildSetting) {
	if Version != devSentinel {
		return // explicitly stamped via -ldflags
	}
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			if s.Value != "" {
				Version = s.Value
			}
		case "vcs.time":
			if s.Value != "" {
				BuildTime = s.Value
			}
		}
	}
}

// applyModuleVersion is the last fallback: `go install pkg@vX.Y.Z` builds from
// the module cache with no VCS context, so it embeds no vcs.* settings at all
// and records the resolved module version in debug.BuildInfo.Main.Version
// instead. Called after applyVCSStamp — the dev-sentinel guard means both an
// -ldflags stamp and a local git build still win. "(devel)" is what a local
// build puts here and is no more useful than the sentinel, so it is rejected.
func applyModuleVersion(mainVersion string) {
	if Version != devSentinel || mainVersion == "" || mainVersion == "(devel)" {
		return
	}
	Version = mainVersion
	applyReleaseStamp(mainVersion, releaseVersion, releaseTime)
}

// applyReleaseStamp supplies BuildTime on that same `go install` path, where no
// build time is recoverable at runtime: the module cache carries no commit time,
// and debug.BuildInfo has no timestamp field. The release pipeline therefore
// bakes one into the source it tags (buildstamp.go, written by
// scripts/write_build_stamp.py during `knope prepare-release`).
//
// relVersion is compared against the resolved module version so the stamp is
// only ever claimed by the exact release it describes: a `go install …@main` or
// …@<sha> build resolves to a pseudo-version (vX.Y.Z-0.<time>-<sha>), doesn't
// match, and keeps unknownTime rather than inheriting the previous release's
// timestamp. The BuildTime guard leaves an -ldflags stamp and a vcs.time stamp
// untouched; empty stamp fields mean "no release stamp in this source".
func applyReleaseStamp(mainVersion, relVersion, relTime string) {
	if BuildTime != unknownTime || relVersion == "" || relTime == "" {
		return
	}
	if mainVersion != "v"+relVersion {
		return
	}
	BuildTime = relTime
}

func main() {
	var (
		serve     = flag.Bool("serve", false, "Self-daemonize and run the RPC server")
		bridge    = flag.Bool("bridge", false, "Connect stdio to the running server")
		stop      = flag.Bool("stop", false, "Stop the running server (server.shutdown RPC)")
		version   = flag.Bool("version", false, "Print version and exit")
		install   = flag.Bool("install", false, "Ensure CLI present, prune old versions, print JSON facts")
		socket    = flag.String("socket", "", "Path to the daemon's Unix socket")
		tokenFile = flag.String("token-file", "", "Read auth token from this file at startup, then unlink it. Used by the daemonized child so the token never appears in /proc/<pid>/environ.")
		tokenFd   = flag.Int("token-fd", -1, "Read the auth token from this already-open file descriptor (e.g. 0 for stdin) instead of -token-file — no temp file touches disk. -serve only.")

		metricsAddr = flag.String("metrics-addr", "", "If set (e.g. 127.0.0.1:9090), serve Prometheus counters at /metrics on this address. Off by default; -serve only. Counts only, no auth — bind to a trusted interface.")

		keepChildren = flag.Bool("keep-children", false, "On graceful shutdown, leave spawned child processes running instead of killing them, so they survive a daemon restart/upgrade. Off by default; -serve only; POSIX-only (ignored with a warning on Windows, where children are confined to a Job Object that the OS terminates on daemon exit). The new daemon does not re-adopt the survivors.")

		maxExtract = flag.Int64("max-extract-bytes", 0, "Cap the total uncompressed bytes files.extract_tar will write, in bytes. 0 (the default) means no cap, which is what the reference does; a non-zero value is an opt-in divergence. -serve only. Claude Desktop owns the argv, so the max-extract-bytes key in claustrum.conf is usually the reachable way to set this.")

		listenPipe = flag.Bool("listen-pipe", false, "Additionally serve the same JSON-RPC over a Windows named pipe (claustrum picks the name and writes it to rpc.pipe beside the socket) so clients that cannot consume the AF_UNIX socket on Windows can still connect. Off by default; -serve only; Windows-only (ignored with a warning elsewhere). Strictly additive — the AF_UNIX socket and the wire contract are unchanged.")

		cliDir      = flag.String("cli-dir", "", "Directory for per-version CLI binaries")
		cliVersion  = flag.String("cli-version", "", "Required CLI version (filename under --cli-dir)")
		cliURL      = flag.String("cli-url", "", "Download URL for the CLI .zst")
		cliChecksum = flag.String("cli-checksum", "", "Expected SHA256 of the compressed CLI .zst")
		cliZst      = flag.String("cli-zst", "", "Path to an already-uploaded CLI .zst (SFTP fallback)")
		cliKeep     = flag.Int("cli-keep", 3, "How many most-recent CLI versions to keep")
	)
	flag.Parse()
	resolveVersion()

	// Optional claustrum.conf next to the binary gates opt-in divergences; absent
	// or malformed → stock defaults. Precedence is explicit CLI flag > config >
	// default, so record which flags the user actually set.
	cfg := loadConfig()
	cliSet := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { cliSet[f.Name] = true })

	// Resolve the user's home directory up front (used for default path resolution);
	// fatal if it can't be determined.
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "claustrum: cannot resolve home directory: %v\n", err)
		osExit(1)
	}
	// When -socket is omitted, -bridge/-stop fall back to the reference's default
	// daemon socket. (The deployment always passes -socket explicitly; this only
	// matters for bare invocations.)
	resolveSocket := func() string {
		if *socket != "" {
			return *socket
		}
		return filepath.Join(home, ".claude", "remote", "rpc.sock")
	}

	switch {
	case *version:
		fmt.Println(versionLine(cfg.versionOverride))
		return
	case *install:
		runInstall(installOpts{
			cliDir: *cliDir, cliVersion: *cliVersion, cliURL: *cliURL,
			cliChecksum: *cliChecksum, cliZst: *cliZst, cliKeep: *cliKeep,
		})
		return
	case *stop:
		// Best-effort: a missing/unreachable daemon is silently a no-op (exit 0).
		_ = runStop(resolveSocket())
		return
	case *bridge:
		if err := runBridge(resolveSocket()); err != nil {
			fmt.Fprintf(os.Stderr, "claustrum: %v\n", err)
			osExit(1)
		}
		return
	case *serve:
		// -serve only: the cap governs files.extract_tar, which only the daemon
		// serves. Set before runServe because the value is read deep in the
		// method, not carried through the server struct.
		maxExtractBytes = cfg.effectiveMaxExtractBytes(*maxExtract, cliSet["max-extract-bytes"])
		runServe(resolveSocket(), *tokenFile, *tokenFd,
			cfg.effectiveMetricsAddr(*metricsAddr, cliSet["metrics-addr"]),
			cfg.effectiveKeepChildren(*keepChildren, cliSet["keep-children"]),
			cfg.effectiveListenPipe(*listenPipe, cliSet["listen-pipe"]))
		return
	default:
		fmt.Fprintln(os.Stderr, "claustrum: one of --version/--install/--serve/--bridge/--stop is required")
		osExit(2)
	}
}
