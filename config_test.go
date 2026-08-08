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
		// An unrecognised value leaves the key unset, so the guard stays OFF —
		// the direction that matters, since a typo must never switch a divergence on.
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
	// Non-aliasing against the other two duration keys, which is the pair that
	// could plausibly be crossed: a fused switch case passes gofmt, vet, lint and
	// the rest of this suite while making one key a dead no-op.
	if got := parse(t, "git-timeout = 60s"); got.cliProbeTimeout != nil || got.cliDownloadTimeout != nil {
		t.Errorf("git-timeout leaked into an -install duration: probe=%v download=%v",
			got.cliProbeTimeout, got.cliDownloadTimeout)
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
// `var filesReadRegularOnly = true` would pass the rest of the suite and silently
// resurrect the -32602 D4's flip removed. (Verified by mutation: this is the sole
// detector.)
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
