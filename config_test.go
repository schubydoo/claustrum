package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
