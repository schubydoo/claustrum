package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	validSHA  = "7c2f88d13e5f269762dd4d463aa4eb3102214110"                         // 40-hex git SHA-1 (the real format)
	validSHA2 = "7c2f88d13e5f269762dd4d463aa4eb3102214110c0ffee0badf00ddeadbeef99" // 64-hex, also accepted
)

func parse(t *testing.T, body string) config {
	t.Helper()
	var cfg config
	parseConfig(strings.NewReader(body), &cfg)
	return cfg
}

func boolp(b bool) *bool { return &b }

func int64p(n int64) *int64 { return &n }

func durp(d time.Duration) *time.Duration { return &d }

func TestParseConfig_VersionOverride(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"valid git sha-1 (40)", "version-override = " + validSHA, validSHA},
		{"valid sha-256 (64)", "version-override = " + validSHA2, validSHA2},
		{"valid upper normalized", "version-override = " + strings.ToUpper(validSHA), validSHA},
		{"with comment + blank lines", "\n# a note\nversion-override=" + validSHA + "\n", validSHA},
		{"too short (7)", "version-override = 7c2f88d", ""},
		{"wrong length (41)", "version-override = " + validSHA + "a", ""},
		{"too long (65)", "version-override = " + validSHA2 + "a", ""},
		{"non-hex", "version-override = " + strings.Repeat("g", 40), ""},
		{"already-prefixed rejected", "version-override = claude-ssh " + validSHA, ""},
		{"empty value", "version-override =", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parse(t, tc.body).versionOverride; got != tc.want {
				t.Fatalf("versionOverride = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseConfig_KeepChildren(t *testing.T) {
	cases := []struct {
		body string
		want *bool
	}{
		{"keep-children = true", boolp(true)},
		{"keep-children = TRUE", boolp(true)},
		{"keep-children = 1", boolp(true)},
		{"keep-children = on", boolp(true)},
		{"keep-children = false", boolp(false)},
		{"keep-children = 0", boolp(false)},
		{"keep-children = maybe", nil},
		{"keep-children =", nil},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			got := parse(t, tc.body).keepChildren
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("keepChildren = %v, want nil", *got)
			case tc.want != nil && (got == nil || *got != *tc.want):
				t.Fatalf("keepChildren = %v, want %v", got, *tc.want)
			}
		})
	}
}

func TestParseConfig_FilesReadRegularOnly(t *testing.T) {
	cases := []struct {
		body string
		want *bool
	}{
		{"files-read-regular-only = true", boolp(true)},
		{"files-read-regular-only = ON", boolp(true)},
		{"files-read-regular-only = 1", boolp(true)},
		{"files-read-regular-only = false", boolp(false)},
		{"files-read-regular-only = 0", boolp(false)},
		// An unrecognised value leaves the key UNSET, which is all this table
		// asserts (nil). What the guard then does is effectiveFilesReadRegularOnly's
		// business and is covered by TestPrecedenceFilesReadRegularOnly: the flag
		// value stands, so "off" only when no flag was passed. Do not restate it
		// here as "the guard stays off" — that claim is not asserted by this test
		// and is false when -files-read-regular-only is also on the argv.
		{"files-read-regular-only = maybe", nil},
		{"files-read-regular-only =", nil},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			got := parse(t, tc.body).filesReadRegularOnly
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("filesReadRegularOnly = %v, want nil", *got)
			case tc.want != nil && (got == nil || *got != *tc.want):
				t.Fatalf("filesReadRegularOnly = %v, want %v", got, *tc.want)
			}
		})
	}
}

func TestParseConfig_ListenPipe(t *testing.T) {
	cases := []struct {
		body string
		want *bool
	}{
		{"listen-pipe = true", boolp(true)},
		{"listen-pipe = ON", boolp(true)},
		{"listen-pipe = 1", boolp(true)},
		{"listen-pipe = false", boolp(false)},
		{"listen-pipe = 0", boolp(false)},
		{"listen-pipe = maybe", nil},
		{"listen-pipe =", nil},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			got := parse(t, tc.body).listenPipe
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("listenPipe = %v, want nil", *got)
			case tc.want != nil && (got == nil || *got != *tc.want):
				t.Fatalf("listenPipe = %v, want %v", got, *tc.want)
			}
		})
	}
}

func TestParseConfig_MetricsAndUnknownAndMalformed(t *testing.T) {
	cfg := parse(t, strings.Join([]string{
		"metrics-addr = 127.0.0.1:9090",
		"unknown-key = whatever",  // ignored
		"this line has no equals", // skipped
		"  # indented comment",
		"KEEP-CHILDREN = true", // key is case-insensitive
	}, "\n"))
	if cfg.metricsAddr != "127.0.0.1:9090" {
		t.Fatalf("metricsAddr = %q", cfg.metricsAddr)
	}
	if cfg.keepChildren == nil || !*cfg.keepChildren {
		t.Fatalf("keepChildren = %v, want true (case-insensitive key)", cfg.keepChildren)
	}
}

func TestParseConfig_MetricsRejectsControlChars(t *testing.T) {
	if got := parse(t, "metrics-addr = 127.0.0.1:9090\x07").metricsAddr; got != "" {
		t.Fatalf("metricsAddr = %q, want \"\" (control char rejected)", got)
	}
}

func TestPrecedence(t *testing.T) {
	withFile := config{keepChildren: boolp(true), metricsAddr: "127.0.0.1:9090"}
	// CLI explicitly set → CLI wins over the file.
	if withFile.effectiveKeepChildren(false, true) != false {
		t.Fatal("explicit CLI -keep-children=false should win over config true")
	}
	if got := withFile.effectiveMetricsAddr("1.2.3.4:1", true); got != "1.2.3.4:1" {
		t.Fatalf("explicit CLI -metrics-addr should win, got %q", got)
	}
	// CLI unset → config fills in.
	if withFile.effectiveKeepChildren(false, false) != true {
		t.Fatal("config keep-children=true should apply when CLI unset")
	}
	if got := withFile.effectiveMetricsAddr("", false); got != "127.0.0.1:9090" {
		t.Fatalf("config metrics-addr should apply when CLI unset, got %q", got)
	}
	// Empty config → default stands.
	if (config{}).effectiveKeepChildren(false, false) != false {
		t.Fatal("empty config should leave keep-children default false")
	}

	// -listen-pipe follows the same precedence rules.
	pipeCfg := config{listenPipe: boolp(true)}
	if pipeCfg.effectiveListenPipe(false, true) != false {
		t.Fatal("explicit CLI -listen-pipe=false should win over config true")
	}
	if pipeCfg.effectiveListenPipe(false, false) != true {
		t.Fatal("config listen-pipe=true should apply when CLI unset")
	}
	if (config{}).effectiveListenPipe(false, false) != false {
		t.Fatal("empty config should leave listen-pipe default false")
	}
	if (config{}).effectiveListenPipe(true, true) != true {
		t.Fatal("explicit CLI -listen-pipe=true should apply with no config")
	}
}

func TestParseConfig_MaxExtractBytes(t *testing.T) {
	cases := []struct {
		name, body string
		want       *int64
	}{
		{"plain byte count", "max-extract-bytes = 536870912", int64p(536870912)},
		{"zero disables the cap explicitly", "max-extract-bytes = 0", int64p(0)},
		{"negative rejected", "max-extract-bytes = -1", nil},
		{"non-numeric rejected", "max-extract-bytes = 512MiB", nil},
		{"empty rejected", "max-extract-bytes =", nil},
		{"case-insensitive key", "MAX-EXTRACT-BYTES = 1024", int64p(1024)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parse(t, tc.body).maxExtractBytes
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("maxExtractBytes = %d, want unset (value rejected)", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("maxExtractBytes unset, want %d", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("maxExtractBytes = %d, want %d", *got, *tc.want)
			}
		})
	}
}

// Separate from the max-extract-bytes test above on purpose: these two keys are
// the pair whose switch cases a careless merge would collapse into one, and two
// independent tests are what would catch that.
func TestParseConfig_MaxCLIBytes(t *testing.T) {
	cases := []struct {
		name, body string
		want       *int64
	}{
		{"plain byte count", "max-cli-bytes = 536870912", int64p(536870912)},
		{"zero disables the cap explicitly", "max-cli-bytes = 0", int64p(0)},
		{"negative rejected", "max-cli-bytes = -1", nil},
		{"non-numeric rejected", "max-cli-bytes = 512MiB", nil},
		{"empty rejected", "max-cli-bytes =", nil},
		{"case-insensitive key", "MAX-CLI-BYTES = 1024", int64p(1024)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parse(t, tc.body).maxCLIBytes
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("maxCLIBytes = %d, want unset (value rejected)", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("maxCLIBytes unset, want %d", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("maxCLIBytes = %d, want %d", *got, *tc.want)
			}
		})
	}
	// The pair must not alias: setting one key must leave the other unset. This is
	// the assertion that fails if the two switch cases are ever collapsed.
	if got := parse(t, "max-cli-bytes = 4096"); got.maxExtractBytes != nil {
		t.Errorf("max-cli-bytes also set maxExtractBytes = %d, want unset", *got.maxExtractBytes)
	}
	if got := parse(t, "max-extract-bytes = 4096"); got.maxCLIBytes != nil {
		t.Errorf("max-extract-bytes also set maxCLIBytes = %d, want unset", *got.maxCLIBytes)
	}
}

// -max-extract-bytes follows the same CLI-over-config-over-default precedence,
// with one wrinkle the others do not have: 0 is a real value (cap off), so
// "config said 0" and "config said nothing" must stay distinguishable.
func TestPrecedenceMaxExtractBytes(t *testing.T) {
	withFile := config{maxExtractBytes: int64p(1024)}
	if got := withFile.effectiveMaxExtractBytes(2048, true); got != 2048 {
		t.Errorf("explicit CLI should win, got %d", got)
	}
	if got := withFile.effectiveMaxExtractBytes(0, false); got != 1024 {
		t.Errorf("config value should apply when CLI unset, got %d", got)
	}
	// An explicit CLI 0 turns the cap off even though the file enabled it.
	if got := withFile.effectiveMaxExtractBytes(0, true); got != 0 {
		t.Errorf("explicit CLI 0 should disable the cap, got %d", got)
	}
	// A config 0 is "set", not "absent" — the pointer is what keeps them apart.
	if got := (config{maxExtractBytes: int64p(0)}).effectiveMaxExtractBytes(0, false); got != 0 {
		t.Errorf("config 0 should apply, got %d", got)
	}
	if got := (config{}).effectiveMaxExtractBytes(0, false); got != 0 {
		t.Errorf("empty config should leave the cap off, got %d", got)
	}
}

// -max-cli-bytes follows the same CLI-over-config-over-default precedence, with
// the same wrinkle the other numeric key has: 0 is a real value (cap off), so
// "config said 0" and "config said nothing" must stay distinguishable.
func TestPrecedenceMaxCLIBytes(t *testing.T) {
	withFile := config{maxCLIBytes: int64p(1024)}
	if got := withFile.effectiveMaxCLIBytes(2048, true); got != 2048 {
		t.Errorf("explicit CLI should win, got %d", got)
	}
	if got := withFile.effectiveMaxCLIBytes(0, false); got != 1024 {
		t.Errorf("config value should apply when CLI unset, got %d", got)
	}
	if got := withFile.effectiveMaxCLIBytes(0, true); got != 0 {
		t.Errorf("explicit CLI 0 should disable the cap, got %d", got)
	}
	if got := (config{maxCLIBytes: int64p(0)}).effectiveMaxCLIBytes(0, false); got != 0 {
		t.Errorf("config 0 should apply, got %d", got)
	}
	if got := (config{}).effectiveMaxCLIBytes(0, false); got != 0 {
		t.Errorf("empty config should leave the cap off, got %d", got)
	}
	// A negative flag normalises to disabled rather than reaching zstdDecompress /
	// fetchToFile as a negative — the asymmetry effectiveNumeric centralises for
	// all six numeric knobs. (The sibling probe/download-timeout tests cover their
	// own negative arms; this pins maxCLIBytes's, which the precedence cases above
	// did not exercise.)
	if got := (config{}).effectiveMaxCLIBytes(-1, true); got != 0 {
		t.Errorf("negative CLI should normalise to 0, got %d", got)
	}
}

// The runnability probe's deadline is the third opt-in numeric key and the first
// that is a duration, so a bare number must be REJECTED rather than silently
// meaning nanoseconds.
func TestParseConfig_CLIProbeTimeout(t *testing.T) {
	cases := []struct {
		name, body string
		want       *time.Duration
	}{
		{"seconds", "cli-probe-timeout = 30s", durp(30 * time.Second)},
		{"minutes", "cli-probe-timeout = 2m", durp(2 * time.Minute)},
		{"negative rejected", "cli-probe-timeout = -1s", nil},
		{"bare number rejected", "cli-probe-timeout = 15", nil},
		// Go's parser special-cases a bare zero, so "0" and "+0" DO parse. Pinned
		// because the docs say "a bare number is rejected" and this is the
		// exception to it — harmless (0 means disabled either way) but real.
		{"bare zero is accepted by Go's parser", "cli-probe-timeout = 0", durp(0)},
		{"bare +0 likewise", "cli-probe-timeout = +0", durp(0)},
		// "-0" and "-0s" are negative in spelling but parse to zero, so they pass
		// the d >= 0 guard and are ACCEPTED rather than dropped. Pinned because
		// "a negative is rejected" is otherwise read as covering them.
		{"negative zero is accepted, not dropped", "cli-probe-timeout = -0", durp(0)},
		{"negative zero with a unit likewise", "cli-probe-timeout = -0s", durp(0)},
		{"any zero-valued duration, any sign", "cli-probe-timeout = -0m", durp(0)},
		// A genuinely negative value that truncates to zero. Not a spelling of
		// zero — it is why "a negative is rejected" is false at the edge.
		{"negative truncating to zero is accepted", "cli-probe-timeout = -0.4ns", durp(0)},
		// The spelling an operator most often writes meaning "disabled".
		{"plain 0s", "cli-probe-timeout = 0s", durp(0)},
		{"non-duration rejected", "cli-probe-timeout = soon", nil},
		{"empty rejected", "cli-probe-timeout =", nil},
		{"case-insensitive key", "CLI-PROBE-TIMEOUT = 45s", durp(45 * time.Second)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parse(t, tc.body).cliProbeTimeout
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("cliProbeTimeout = %s, want unset (value rejected)", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("cliProbeTimeout unset, want %s", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("cliProbeTimeout = %s, want %s", *got, *tc.want)
			}
		})
	}
	// Same non-aliasing assertion the two byte-count keys carry: this key must not
	// reach either of them, and neither of them must reach it.
	// "0" on purpose, not "30s": a duration-only spelling fails ParseInt, so it
	// could never observe a leak into the int64 keys. 0 parses both ways.
	if got := parse(t, "cli-probe-timeout = 0"); got.maxCLIBytes != nil || got.maxExtractBytes != nil {
		t.Errorf("cli-probe-timeout leaked into a size cap: cli=%v extract=%v", got.maxCLIBytes, got.maxExtractBytes)
	}
	if got := parse(t, "max-cli-bytes = 4096"); got.cliProbeTimeout != nil {
		t.Errorf("max-cli-bytes also set cliProbeTimeout = %s, want unset", *got.cliProbeTimeout)
	}
	// ...and against the OTHER duration key, which the int64 assertions above
	// cannot cover. Unioning the two duration cases into one body that sets both
	// fields is not a dead no-op — it is cross-contamination: opting into D11
	// would silently switch D12's download bound on at the same value. That mutant
	// passes gofmt, vet, golangci-lint and the whole suite without this line.
	if got := parse(t, "cli-probe-timeout = 30s"); got.cliDownloadTimeout != nil {
		t.Errorf("cli-probe-timeout also set cliDownloadTimeout = %s, want unset", *got.cliDownloadTimeout)
	}
}

// -cli-probe-timeout follows the same CLI-over-config-over-default precedence as
// the two size caps, with the same 0-is-a-real-value wrinkle.
func TestPrecedenceCLIProbeTimeout(t *testing.T) {
	withFile := config{cliProbeTimeout: durp(20 * time.Second)}
	if got := withFile.effectiveCLIProbeTimeout(45*time.Second, true); got != 45*time.Second {
		t.Errorf("explicit CLI should win, got %s", got)
	}
	if got := withFile.effectiveCLIProbeTimeout(0, false); got != 20*time.Second {
		t.Errorf("config value should apply when CLI unset, got %s", got)
	}
	if got := withFile.effectiveCLIProbeTimeout(0, true); got != 0 {
		t.Errorf("explicit CLI 0 should disable the deadline, got %s", got)
	}
	if got := (config{cliProbeTimeout: durp(0)}).effectiveCLIProbeTimeout(0, false); got != 0 {
		t.Errorf("config 0 should apply, got %s", got)
	}
	if got := (config{}).effectiveCLIProbeTimeout(0, false); got != 0 {
		t.Errorf("empty config should leave the deadline off, got %s", got)
	}
	// A negative flag normalises to disabled rather than reaching isRunnable as a
	// negative, where context.WithTimeout would expire the probe immediately.
	if got := (config{}).effectiveCLIProbeTimeout(-1*time.Second, true); got != 0 {
		t.Errorf("negative CLI should normalise to 0, got %s", got)
	}
}

// The download bound is the second duration-valued key (cli-probe-timeout, D11,
// came first), so a bare number must be REJECTED rather than silently meaning
// nanoseconds — with the zero exception. Kept separate from the probe-timeout test
// on purpose: these two are the pair a careless merge would collapse into one
// case, and two independent tests are what would catch that.
func TestParseConfig_CLIDownloadTimeout(t *testing.T) {
	cases := []struct {
		name, body string
		want       *time.Duration
	}{
		{"minutes", "cli-download-timeout = 10m", durp(10 * time.Minute)},
		{"seconds", "cli-download-timeout = 90s", durp(90 * time.Second)},
		{"zero disables the bound explicitly", "cli-download-timeout = 0s", durp(0)},
		{"negative rejected", "cli-download-timeout = -1s", nil},
		{"bare number rejected", "cli-download-timeout = 300", nil},
		{"non-duration rejected", "cli-download-timeout = soon", nil},
		{"empty rejected", "cli-download-timeout =", nil},
		{"case-insensitive key", "CLI-DOWNLOAD-TIMEOUT = 2m", durp(2 * time.Minute)},
		// Go's parser accepts zero in unboundedly many spellings, including
		// negative ones and a negative that truncates. All mean disabled, so none
		// can switch the bound on — but "a bare number is rejected" and "a negative
		// is dropped" are both false at these edges.
		{"bare zero accepted", "cli-download-timeout = 0", durp(0)},
		{"negative zero accepted", "cli-download-timeout = -0", durp(0)},
		{"negative zero with a unit accepted", "cli-download-timeout = -0m", durp(0)},
		{"negative truncating to zero accepted", "cli-download-timeout = -0.4ns", durp(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parse(t, tc.body).cliDownloadTimeout
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("cliDownloadTimeout = %s, want unset (value rejected)", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("cliDownloadTimeout unset, want %s", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("cliDownloadTimeout = %s, want %s", *got, *tc.want)
			}
		})
	}
	// Non-aliasing, with a value that parses BOTH ways: a duration-only spelling
	// fails ParseInt and so could never observe a leak into the int64 keys.
	if got := parse(t, "cli-download-timeout = 0"); got.maxCLIBytes != nil || got.maxExtractBytes != nil {
		t.Errorf("cli-download-timeout leaked into a size cap: cli=%v extract=%v", got.maxCLIBytes, got.maxExtractBytes)
	}
	if got := parse(t, "max-cli-bytes = 4096"); got.cliDownloadTimeout != nil {
		t.Errorf("max-cli-bytes also set cliDownloadTimeout = %s, want unset", *got.cliDownloadTimeout)
	}
	// The mirror of the cross-assertion in TestParseConfig_CLIProbeTimeout; see
	// the note there for why the int64 pair cannot stand in for it.
	if got := parse(t, "cli-download-timeout = 10m"); got.cliProbeTimeout != nil {
		t.Errorf("cli-download-timeout also set cliProbeTimeout = %s, want unset", *got.cliProbeTimeout)
	}
}

// -cli-download-timeout follows the same CLI-over-config-over-default precedence
// as the size caps, with the same 0-is-a-real-value wrinkle.
func TestPrecedenceCLIDownloadTimeout(t *testing.T) {
	withFile := config{cliDownloadTimeout: durp(10 * time.Minute)}
	if got := withFile.effectiveCLIDownloadTimeout(30*time.Second, true); got != 30*time.Second {
		t.Errorf("explicit CLI should win, got %s", got)
	}
	if got := withFile.effectiveCLIDownloadTimeout(0, false); got != 10*time.Minute {
		t.Errorf("config value should apply when CLI unset, got %s", got)
	}
	if got := withFile.effectiveCLIDownloadTimeout(0, true); got != 0 {
		t.Errorf("explicit CLI 0 should disable the bound, got %s", got)
	}
	if got := (config{cliDownloadTimeout: durp(0)}).effectiveCLIDownloadTimeout(0, false); got != 0 {
		t.Errorf("config 0 should apply, got %s", got)
	}
	if got := (config{}).effectiveCLIDownloadTimeout(0, false); got != 0 {
		t.Errorf("empty config should leave the bound off, got %s", got)
	}
	if got := (config{}).effectiveCLIDownloadTimeout(-1*time.Second, true); got != 0 {
		t.Errorf("negative CLI should normalise to 0, got %s", got)
	}
}

func TestVersionLine(t *testing.T) {
	oldV, oldB := Version, BuildTime
	Version, BuildTime = "1.4.0", "2026-07-02T00:00:00Z"
	defer func() { Version, BuildTime = oldV, oldB }()

	if got, want := versionLine(""), "claustrum 1.4.0 (built 2026-07-02T00:00:00Z)"; got != want {
		t.Fatalf("default: got %q, want %q", got, want)
	}
	got := versionLine(validSHA)
	want := "claude-ssh " + validSHA + " (via Claustrum 1.4.0, built 2026-07-02T00:00:00Z)"
	if got != want {
		t.Fatalf("override: got %q, want %q", got, want)
	}
}

func TestLoadConfigFrom(t *testing.T) {
	t.Run("valid file parsed", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, configFileName),
			[]byte("version-override = "+validSHA+"\nkeep-children = true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := loadConfigFrom(dir)
		if cfg.versionOverride != validSHA {
			t.Fatalf("versionOverride = %q, want %q", cfg.versionOverride, validSHA)
		}
		if cfg.keepChildren == nil || !*cfg.keepChildren {
			t.Fatalf("keepChildren = %v, want true", cfg.keepChildren)
		}
	})
	t.Run("absent file => zero config", func(t *testing.T) {
		if cfg := loadConfigFrom(t.TempDir()); cfg != (config{}) {
			t.Fatalf("absent: got %+v, want zero config", cfg)
		}
	})
	// A non-regular file (directory at the config path) is treated as absent —
	// cross-platform stand-in for the FIFO/device rejection.
	t.Run("directory => zero config", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, configFileName), 0o700); err != nil {
			t.Fatal(err)
		}
		if cfg := loadConfigFrom(dir); cfg != (config{}) {
			t.Fatalf("directory: got %+v, want zero config", cfg)
		}
	})
}

// loadConfig resolves the executable's own directory, which (in the test binary)
// holds no claustrum.conf, so it must return the zero config without error.
func TestLoadConfig_NoFileNextToBinary(t *testing.T) {
	if cfg := loadConfig(); cfg != (config{}) {
		t.Fatalf("loadConfig() = %+v, want zero config (no conf beside test binary)", cfg)
	}
}

func TestExecutableDir(t *testing.T) {
	dir, ok := executableDir()
	if !ok || dir == "" {
		t.Fatalf("executableDir() = (%q, %v), want a non-empty dir", dir, ok)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("executableDir() = %q, not a directory (err=%v)", dir, err)
	}
}

// A first-line-only oversize file is bounded; the reader cap must not panic and
// parsing still yields the valid keys before the giant tail.
func TestParseConfig_HugeTailBounded(t *testing.T) {
	body := "version-override = " + validSHA + "\n" + strings.Repeat("x=y\n", 100000)
	if got := parse(t, body).versionOverride; got != validSHA {
		t.Fatalf("versionOverride = %q, want %q", got, validSHA)
	}
}

// A negative -max-extract-bytes is normalised to 0 (cap disabled) rather than
// reaching the daemon as a negative. The config path already rejects a negative
// so a typo cannot silently enable a cap; the flag had no validation, and the two
// paths disagreeing about the same input is the gap this closes.
func TestEffectiveMaxExtractBytesNormalisesNegative(t *testing.T) {
	var cfg config
	if got := cfg.effectiveMaxExtractBytes(-1, true); got != 0 {
		t.Errorf("effectiveMaxExtractBytes(-1, true) = %d, want 0", got)
	}
	if got := cfg.effectiveMaxExtractBytes(1024, true); got != 1024 {
		t.Errorf("effectiveMaxExtractBytes(1024, true) = %d, want 1024", got)
	}
	// The config value still wins when the flag was not set.
	n := int64(4096)
	cfg.maxExtractBytes = &n
	if got := cfg.effectiveMaxExtractBytes(0, false); got != 4096 {
		t.Errorf("config value = %d, want 4096", got)
	}
}

// effectiveWireLog ~-expands the resolved path. The config key is sold as the
// reachable knob for a Desktop-launched daemon, so `wire-log = ~/wire.jsonl` is a
// natural thing to write, and an unexpanded ~ would fail the (deliberately fatal)
// open and stop the daemon booting.
func TestEffectiveWireLogExpandsTilde(t *testing.T) {
	cfg := config{wireLog: "~/captures/wire.jsonl"}
	got := cfg.effectiveWireLog("", false)
	if strings.HasPrefix(got, "~") {
		t.Errorf("effectiveWireLog did not expand ~: %q", got)
	}
	if !strings.HasSuffix(got, filepath.Join("captures", "wire.jsonl")) {
		t.Errorf("effectiveWireLog = %q, want it to end in captures/wire.jsonl", got)
	}
	// An explicit CLI value wins and is also expanded.
	if got := cfg.effectiveWireLog("~/cli.jsonl", true); strings.HasPrefix(got, "~") {
		t.Errorf("CLI value not expanded: %q", got)
	}
	// Off stays off (no path, no expansion).
	if got := (config{}).effectiveWireLog("", false); got != "" {
		t.Errorf("empty path became %q, want empty", got)
	}
}

func TestParseConfig_WireLog(t *testing.T) {
	if got := parse(t, "wire-log = /var/log/wire.jsonl").wireLog; got != "/var/log/wire.jsonl" {
		t.Errorf("wireLog = %q, want the path", got)
	}
	if got := parse(t, "wire-log =").wireLog; got != "" {
		t.Errorf("empty wire-log accepted: %q", got)
	}
	i64p := func(n int64) *int64 { return &n }
	cases := []struct {
		name, body string
		want       *int64
	}{
		{"zero keeps everything", "wire-log-max-string = 0", i64p(0)},
		{"positive kept", "wire-log-max-string = 256", i64p(256)},
		{"negative rejected", "wire-log-max-string = -1", nil},
		{"non-numeric rejected", "wire-log-max-string = lots", nil},
		{"empty rejected", "wire-log-max-string =", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parse(t, tc.body).wireLogMaxString
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("wireLogMaxString = %d, want unset (rejected)", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("wireLogMaxString unset, want %d", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("wireLogMaxString = %d, want %d", *got, *tc.want)
			}
		})
	}
}

func TestParseConfig_GitTimeout(t *testing.T) {
	cases := []struct {
		name, body string
		want       *time.Duration
	}{
		{"seconds", "git-timeout = 60s", durp(60 * time.Second)},
		{"minutes", "git-timeout = 2m", durp(2 * time.Minute)},
		{"zero disables the deadline explicitly", "git-timeout = 0s", durp(0)},
		{"negative rejected", "git-timeout = -1s", nil},
		{"bare number rejected", "git-timeout = 60", nil},
		{"non-duration rejected", "git-timeout = soon", nil},
		{"empty rejected", "git-timeout =", nil},
		{"case-insensitive key", "GIT-TIMEOUT = 90s", durp(90 * time.Second)},
		// Same zero-spelling edges as the two -install durations: all mean disabled,
		// so none of them can switch the deadline on.
		{"bare zero accepted", "git-timeout = 0", durp(0)},
		{"negative zero accepted", "git-timeout = -0", durp(0)},
		{"negative zero with a unit accepted", "git-timeout = -0m", durp(0)},
		{"negative truncating to zero accepted", "git-timeout = -0.4ns", durp(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parse(t, tc.body).gitTimeout
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("gitTimeout = %s, want unset (value rejected)", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("gitTimeout unset, want %s", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("gitTimeout = %s, want %s", *got, *tc.want)
			}
		})
	}
	// Non-aliasing against the other duration keys, which is the set that could
	// plausibly be crossed: a fused switch case passes gofmt, vet, lint and the rest
	// of this suite while making one key a dead no-op.
	//
	// ⚠️ libcProbeTimeout is checked here as well as in
	// TestParseConfig_ProbeTimeoutsDoNotCross, because that test only covers the
	// libc→git direction. Without this the REVERSE mutant survives: a fused
	// `case "git-timeout":` that also set cfg.libcProbeTimeout leaves every other
	// assertion green, since TestParseConfig_LibcProbeTimeout only ever feeds
	// libc-probe-timeout bodies. The libc case sits directly beneath git-timeout in
	// applyConfigKey, so this is the adjacency most likely to be copy-pasted.
	if got := parse(t, "git-timeout = 60s"); got.cliProbeTimeout != nil ||
		got.cliDownloadTimeout != nil || got.libcProbeTimeout != nil {
		t.Errorf("git-timeout leaked into an -install duration: probe=%v download=%v libc=%v",
			got.cliProbeTimeout, got.cliDownloadTimeout, got.libcProbeTimeout)
	}
	if got := parse(t, "cli-probe-timeout = 20s"); got.gitTimeout != nil {
		t.Errorf("cli-probe-timeout also set gitTimeout = %s, want unset", *got.gitTimeout)
	}
}

// -git-timeout follows the same CLI-over-config-over-default precedence as the
// other three opt-in knobs, with the same 0-is-a-real-value wrinkle.
func TestPrecedenceGitTimeout(t *testing.T) {
	withFile := config{gitTimeout: durp(60 * time.Second)}
	if got := withFile.effectiveGitTimeout(90*time.Second, true); got != 90*time.Second {
		t.Errorf("explicit CLI should win, got %s", got)
	}
	if got := withFile.effectiveGitTimeout(0, false); got != 60*time.Second {
		t.Errorf("config value should apply when CLI unset, got %s", got)
	}
	if got := withFile.effectiveGitTimeout(0, true); got != 0 {
		t.Errorf("explicit CLI 0 should disable the deadline, got %s", got)
	}
	if got := (config{}).effectiveGitTimeout(0, false); got != 0 {
		t.Errorf("empty config should leave the deadline OFF — that is the parity default, got %s", got)
	}
	if got := (config{}).effectiveGitTimeout(-1*time.Second, true); got != 0 {
		t.Errorf("negative CLI should normalise to 0, got %s", got)
	}
}

// The bypass, asserted directly. "Off" must mean context.WithTimeout is never
// called — not a huge-but-finite deadline — so that exec.CommandContext has no
// cancel path to fire and gitDeadline's timedOut is false by construction.
//
// This is the assertion a "simplify 0 into 100 years" refactor fails: such a
// context still reports a deadline, and this test says it must not.
func TestGitCtxArmsNoDeadlineWhenOff(t *testing.T) {
	old := gitTimeout
	t.Cleanup(func() { gitTimeout = old })

	for _, off := range []time.Duration{0, -1 * time.Second} {
		gitTimeout = off
		ctx, cancel := gitCtx()
		_, hasDeadline := ctx.Deadline()
		cancel()
		if hasDeadline {
			t.Errorf("gitTimeout = %s: context carries a deadline, want none armed at all", off)
		}
		if ctx.Done() != nil {
			t.Errorf("gitTimeout = %s: context is cancellable, want a plain Background context", off)
		}
	}

	gitTimeout = 30 * time.Second
	ctx, cancel := gitCtx()
	defer cancel()
	dl, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		t.Fatal("gitTimeout = 30s: no deadline armed, want one")
	}
	if remaining := time.Until(dl); remaining <= 0 || remaining > 30*time.Second {
		t.Errorf("deadline is %s away, want (0, 30s]", remaining)
	}
}

func TestParseConfig_LibcProbeTimeout(t *testing.T) {
	cases := []struct {
		name, body string
		want       *time.Duration
	}{
		{"seconds", "libc-probe-timeout = 5s", durp(5 * time.Second)},
		{"minutes", "libc-probe-timeout = 1m", durp(time.Minute)},
		{"zero disables", "libc-probe-timeout = 0", durp(0)},
		{"zero with a unit", "libc-probe-timeout = 0s", durp(0)},
		{"negative rejected", "libc-probe-timeout = -1s", nil},
		{"bare number rejected", "libc-probe-timeout = 5", nil},
		{"garbage rejected", "libc-probe-timeout = soon", nil},
		{"empty rejected", "libc-probe-timeout =", nil},
		{"case-insensitive key", "LIBC-PROBE-TIMEOUT = 5s", durp(5 * time.Second)},
		// The zero spellings the case comment cross-references when it says "the
		// same zero/negative edges as cli-probe-timeout above". All of them reach
		// d == 0, which IS the disabled value, so none can switch the deadline on.
		{"negative zero accepted", "libc-probe-timeout = -0", durp(0)},
		{"negative zero with a unit accepted", "libc-probe-timeout = -0m", durp(0)},
		{"negative that truncates to zero accepted", "libc-probe-timeout = -0.4ns", durp(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parse(t, tc.body).libcProbeTimeout
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("libcProbeTimeout = %s, want unset (value rejected)", *got)
			case tc.want != nil && (got == nil || *got != *tc.want):
				t.Fatalf("libcProbeTimeout = %v, want %s", got, *tc.want)
			}
		})
	}
}

// The two probe keys are near-twins (cli/libc prefix) and the same type, so a copy-paste in
// applyConfigKey would have one key populate the other's field. Assert the
// separation at the parse layer, as max-cli-bytes/max-extract-bytes already do.
func TestParseConfig_ProbeTimeoutsDoNotCross(t *testing.T) {
	got := parse(t, "libc-probe-timeout = 5s")
	if got.cliProbeTimeout != nil {
		t.Errorf("libc-probe-timeout also set cliProbeTimeout = %s, want unset", *got.cliProbeTimeout)
	}
	got = parse(t, "cli-probe-timeout = 5s")
	if got.libcProbeTimeout != nil {
		t.Errorf("cli-probe-timeout also set libcProbeTimeout = %s, want unset", *got.libcProbeTimeout)
	}
	// ⚠️ The pair above is the FLAG-swap hazard. The COPY-PASTE hazard is a
	// different neighbour: in applyConfigKey the libc case is written directly
	// beneath `git-timeout`, not beneath `cli-probe-timeout`, so the git case is
	// the one physically adjacent to it. A body that sets only the wrong field is
	// already caught by TestParseConfig_LibcProbeTimeout; the mutant that survives
	// is a body that sets BOTH — which is exactly the union the cli-probe-timeout
	// case warns is "not a dead no-op".
	got = parse(t, "libc-probe-timeout = 5s")
	if got.gitTimeout != nil || got.cliDownloadTimeout != nil {
		t.Errorf("libc-probe-timeout leaked into a neighbour: gitTimeout=%v cliDownloadTimeout=%v",
			got.gitTimeout, got.cliDownloadTimeout)
	}
}

func TestPrecedenceLibcProbeTimeout(t *testing.T) {
	withFile := config{libcProbeTimeout: durp(5 * time.Second)}
	if got := withFile.effectiveLibcProbeTimeout(9*time.Second, true); got != 9*time.Second {
		t.Errorf("explicit CLI should win, got %s", got)
	}
	if got := withFile.effectiveLibcProbeTimeout(0, false); got != 5*time.Second {
		t.Errorf("config value should apply when CLI unset, got %s", got)
	}
	if got := withFile.effectiveLibcProbeTimeout(0, true); got != 0 {
		t.Errorf("explicit CLI 0 should disable the deadline, got %s", got)
	}
	if got := (config{}).effectiveLibcProbeTimeout(0, false); got != 0 {
		t.Errorf("empty config should leave the deadline OFF — that is the parity default, got %s", got)
	}
	if got := (config{}).effectiveLibcProbeTimeout(-1*time.Second, true); got != 0 {
		t.Errorf("negative CLI should normalise to 0, got %s", got)
	}
}

// The bypass, asserted directly — the assertion a "simplify 0 into 100 years"
// refactor fails. Off must mean context.WithTimeout is never called, so
// exec.CommandContext has no cancel path and cannot kill a slow ldd at all.
func TestLddCtxArmsNoDeadlineWhenOff(t *testing.T) {
	for _, off := range []time.Duration{0, -1 * time.Second} {
		ctx, cancel := lddCtx(off)
		_, hasDeadline := ctx.Deadline()
		done := ctx.Done()
		cancel()
		if hasDeadline {
			t.Errorf("lddCtx(%s): context carries a deadline, want none armed at all", off)
		}
		if done != nil {
			t.Errorf("lddCtx(%s): context is cancellable, want a plain Background context", off)
		}
	}
	ctx, cancel := lddCtx(30 * time.Second)
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("lddCtx(30s): no deadline armed, want one")
	}
	if remaining := time.Until(dl); remaining <= 0 || remaining > 30*time.Second {
		t.Errorf("deadline is %s away, want (0, 30s]", remaining)
	}
}

// The SHIPPED default, pinned. Every other test that exercises the probe injects a
// timeout into detectLibcWith directly, so without this, reinstating
// `var lddProbeTimeout = 5 * time.Second` would pass the whole suite.
//
// ⚠️ Scope, and the same scoping TestFilesReadRegularOnlyDefaultIsOff carries — this
// comment got it wrong once already and is corrected here: that mutation is a
// TEST-SUITE hazard, not a shipped-binary one. main.go's -install arm assigns
// lddProbeTimeout unconditionally before runInstall, and detectLibc has exactly one
// non-test caller (inside runInstall), so the package-var initialiser is overwritten
// on every real -install and never reaches production. The detector for the SHIPPED
// default is the f.DefValue != "0s" assertion in main_dispatch_test.go. Both are
// worth having; they catch different mutations.
func TestLddProbeTimeoutDefaultIsOff(t *testing.T) {
	if lddProbeTimeout != 0 {
		t.Errorf("package default lddProbeTimeout = %s, want 0 — the deadline must ship OFF (D14)", lddProbeTimeout)
	}
	ctx, cancel := lddCtx(lddProbeTimeout)
	defer cancel()
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		t.Error("the default context carries a deadline; at the shipped default none may be armed")
	}
}

func TestPrecedenceFilesReadRegularOnly(t *testing.T) {
	withFile := config{filesReadRegularOnly: boolp(true)}
	if got := withFile.effectiveFilesReadRegularOnly(false, true); got {
		t.Error("explicit CLI -files-read-regular-only=false should win over config true")
	}
	if got := withFile.effectiveFilesReadRegularOnly(false, false); !got {
		t.Error("config files-read-regular-only=true should apply when CLI unset")
	}
	if got := (config{filesReadRegularOnly: boolp(false)}).effectiveFilesReadRegularOnly(true, true); !got {
		t.Error("explicit CLI true should win over config false")
	}
	if got := (config{}).effectiveFilesReadRegularOnly(false, false); got {
		t.Error("empty config should leave the guard OFF — that is the parity default")
	}
}

// The SHIPPED default, pinned. Every test that exercises the guard assigns
// filesReadRegularOnly explicitly, and no test asks effectiveFilesReadRegularOnly
// what to do with an UNSET flag and an unset config — the one combination that
// yields the shipped value — so without this, reinstating
// `var filesReadRegularOnly = true` would pass the rest of the suite. (Verified by
// mutation: this is the sole detector.)
//
// ⚠️ Scope, because an earlier version of this comment overstated it: that mutation
// is a TEST-SUITE hazard, not a shipped-binary one. main.go's -serve arm assigns
// filesReadRegularOnly unconditionally and is the only non-test writer, so the
// package-var initialiser is dead in production. The detector for the SHIPPED
// default is the f.DefValue != "false" assertion in main_dispatch_test.go. Both
// are needed; they catch different mutations.
//
// Order-safe: every test that changes filesReadRegularOnly restores it with
// t.Cleanup, and none of them call t.Parallel.
func TestFilesReadRegularOnlyDefaultIsOff(t *testing.T) {
	if filesReadRegularOnly {
		t.Error("package default filesReadRegularOnly = true, want false — the guard must ship OFF (D4)")
	}
}

// The SHIPPED default, pinned. Every other test in this package assigns
// gitTimeout explicitly, and effectiveGitTimeout is only ever asked what to do
// with a zero flag value — so without this, reinstating
// `var gitTimeout = 60 * time.Second` would pass the entire suite and silently
// resurrect every claustrum-only frame D5's flip removed.
//
// Order-safe: every test that changes gitTimeout restores it with t.Cleanup, and
// none of them call t.Parallel.
func TestGitTimeoutDefaultIsOff(t *testing.T) {
	if gitTimeout != 0 {
		t.Errorf("package default gitTimeout = %s, want 0 — the deadline must ship OFF (D5)", gitTimeout)
	}
	ctx, cancel := gitCtx()
	defer cancel()
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		t.Error("the default context carries a deadline; at the shipped default none may be armed")
	}
}

// isPrintableASCII gates the `metrics-addr` and `wire-log` config values, so its
// two bounds decide whether a legitimate setting is kept or silently dropped —
// the key is skipped without a diagnostic, and the operator sees a daemon that
// ignored their config. Both ends are inclusive and both are one byte wide, so a
// test that only checks "letters pass, \x00 fails" cannot see either bound move:
// 0x20 is SPACE and 0x7E is '~', and a wire-log path plausibly contains both.
func TestIsPrintableASCIIBoundsAreInclusive(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"0x1f just below the low bound", "\x1f", false},
		{"0x20 SPACE is the low bound", " ", true},
		{"0x7e tilde is the high bound", "~", true},
		{"0x7f DEL just above the high bound", "\x7f", false},
		{"a realistic path keeping both bounds", "~/wire log.jsonl", true},
		{"an embedded control byte anywhere fails", "ok\x00ok", false},
	} {
		if got := isPrintableASCII(tc.in); got != tc.want {
			t.Errorf("%s: isPrintableASCII(%q) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}
