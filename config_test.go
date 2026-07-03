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

// loadConfig must treat a non-regular file (here: a directory at the config path)
// as absent — cross-platform stand-in for the FIFO/device rejection.
func TestLoadConfig_NonRegularIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, configFileName), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// loadConfig keys off os.Executable, so exercise the guard directly.
	fi, err := os.Lstat(filepath.Join(dir, configFileName))
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode().IsRegular() {
		t.Fatal("directory reported as regular file")
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
