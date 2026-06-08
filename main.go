// Command claustrum is a clean-room reimplementation of the small Go daemon that
// drives a remote Claude Code session: a local CLI-version manager + process
// supervisor + JSON-RPC multiplexer (with a replay buffer) over an AF_UNIX socket.
//
// It was built to a behavioral contract captured by probing the reference binary;
// no code was copied or decompiled.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
)

// Version is the daemon's self-reported version (the real binary reports its vcs
// revision hash here, e.g. a 40-char git SHA). When not overridden via
// -ldflags "-X main.Version=...", it is auto-derived from the embedded VCS build
// info (git revision + commit time), matching how the real binary is stamped.
var (
	Version   = "claustrum-dev"
	BuildTime = "unknown"
)

// resolveVersion populates Version/BuildTime from embedded VCS info unless they
// were already set at build time via -ldflags.
func resolveVersion() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	applyVCSStamp(bi.Settings)
}

// applyVCSStamp fills Version/BuildTime from the embedded VCS build settings,
// unless Version was already stamped via -ldflags (i.e. it differs from the dev
// sentinel). Split out from resolveVersion so the stamp logic is testable
// without depending on the test binary's own (absent) VCS metadata.
func applyVCSStamp(settings []debug.BuildSetting) {
	if Version != "claustrum-dev" {
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

func main() {
	var (
		serve     = flag.Bool("serve", false, "Self-daemonize and run the RPC server")
		bridge    = flag.Bool("bridge", false, "Connect stdio to the running server")
		stop      = flag.Bool("stop", false, "Stop the running server (server.shutdown RPC)")
		version   = flag.Bool("version", false, "Print version and exit")
		install   = flag.Bool("install", false, "Ensure CLI present, prune old versions, print JSON facts")
		socket    = flag.String("socket", "", "Path to the daemon's Unix socket")
		tokenFile = flag.String("token-file", "", "Read auth token from this file at startup, then unlink it. Used by the daemonized child so the token never appears in /proc/<pid>/environ.")

		cliDir      = flag.String("cli-dir", "", "Directory for per-version CLI binaries")
		cliVersion  = flag.String("cli-version", "", "Required CLI version (filename under --cli-dir)")
		cliURL      = flag.String("cli-url", "", "Download URL for the CLI .zst")
		cliChecksum = flag.String("cli-checksum", "", "Expected SHA256 of the compressed CLI .zst")
		cliZst      = flag.String("cli-zst", "", "Path to an already-uploaded CLI .zst (SFTP fallback)")
		cliKeep     = flag.Int("cli-keep", 3, "How many most-recent CLI versions to keep")
	)
	flag.Parse()
	resolveVersion()

	// Resolve the user's home directory up front (used for default path resolution);
	// fatal if it can't be determined.
	if _, err := os.UserHomeDir(); err != nil {
		fmt.Fprintf(os.Stderr, "claustrum: cannot resolve home directory: %v\n", err)
		os.Exit(1)
	}

	switch {
	case *version:
		fmt.Printf("claustrum %s (built %s)\n", Version, BuildTime)
		return
	case *install:
		runInstall(installOpts{
			cliDir: *cliDir, cliVersion: *cliVersion, cliURL: *cliURL,
			cliChecksum: *cliChecksum, cliZst: *cliZst, cliKeep: *cliKeep,
		})
		return
	case *stop:
		if err := runStop(*socket); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	case *bridge:
		if err := runBridge(*socket); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	case *serve:
		runServe(*socket, *tokenFile)
		return
	default:
		fmt.Fprintln(os.Stderr, "claustrum: one of --version/--install/--serve/--bridge/--stop is required")
		flag.Usage()
		os.Exit(2)
	}
}
