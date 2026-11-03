package git

import (
	"slices"
	"testing"
)

// TestWorktreeConfigItems pins which settings of a `config.worktree` file go to the
// git calls as `-c` items, and their form. 5fd08069 sends `core.filemode=false` and
// `core.sparsecheckout=true` (measured on Linux). It sends `core.filemode=true`
// (measured on Windows) and `core.eol=lf`. It sends no item for `core.bare` and
// `core.worktree` (measured). The six rows of those names are measured on Linux. A
// measurement has the four keys with an item on macOS. The Windows extra rows are
// measured on Windows. Four keys give an item, and `core.hookspath` and `user.name`
// give none. One more case is measured on Linux. The rows marked not measured follow
// copyKeyRules.
func TestWorktreeConfigItems(t *testing.T) {
	set := func(key, value string) repoSetting { return repoSetting{key: key, value: value, hasValue: true} }
	for _, row := range []struct {
		name     string
		settings []repoSetting
		want     []string
	}{
		{"12a: bare, filemode and sparsecheckout",
			[]repoSetting{set("core.bare", "false"), set("core.filemode", "false"), set("core.sparsecheckout", "true")},
			[]string{"-c", "core.filemode=false", "-c", "core.sparsecheckout=true"}},
		{"12c: filemode alone", []repoSetting{set("core.filemode", "false")}, []string{"-c", "core.filemode=false"}},
		{"N16c: filemode = true", []repoSetting{set("core.filemode", "true")}, []string{"-c", "core.filemode=true"}},
		{"12d: bare and worktree", []repoSetting{set("core.bare", "false"), set("core.worktree", "/x/repo")}, nil},
		{"9a: bare = true", []repoSetting{set("core.bare", "true")}, nil},
		{"1c: eol", []repoSetting{set("core.eol", "lf")}, []string{"-c", "core.eol=lf"}},
		{"sorted by key (not measured)", []repoSetting{set("core.sparsecheckout", "true"), set("core.filemode", "false")},
			[]string{"-c", "core.filemode=false", "-c", "core.sparsecheckout=true"}},
		{"p2-K-autocrlf-input", []repoSetting{set("core.autocrlf", "input")}, []string{"-c", "core.autocrlf=input"}},
		{"p2-K-ignorecase-true", []repoSetting{set("core.ignorecase", "true")}, []string{"-c", "core.ignorecase=true"}},
		{"p2-K-symlinks-false", []repoSetting{set("core.symlinks", "false")}, []string{"-c", "core.symlinks=false"}},
		{"p2-K-sparsecheckoutcone-true", []repoSetting{set("core.sparsecheckoutcone", "true")}, []string{"-c", "core.sparsecheckoutcone=true"}},
		{"p2-K-hookspath-x", []repoSetting{set("core.hookspath", "/x")}, nil},
		{"p2-K-user-name-n", []repoSetting{set("user.name", "n")}, nil},
		{"Windows extra row: safecrlf", []repoSetting{set("core.safecrlf", "true")}, []string{"-c", "core.safecrlf=true"}},
		{"Windows extra row: quotepath", []repoSetting{set("core.quotepath", "false")}, []string{"-c", "core.quotepath=false"}},
		{"Windows extra row: precomposeunicode", []repoSetting{set("core.precomposeunicode", "true")}, []string{"-c", "core.precomposeunicode=true"}},
		{"Windows extra row: untrackedcache", []repoSetting{set("core.untrackedcache", "true")}, []string{"-c", "core.untrackedcache=true"}},
		{"Windows extra row: longpaths", []repoSetting{set("core.longpaths", "true")}, []string{"-c", "core.longpaths=true"}},
		{"Windows extra row: protectntfs", []repoSetting{set("core.protectntfs", "false")}, nil},
		{"Windows extra row: status.showuntrackedfiles", []repoSetting{set("status.showuntrackedfiles", "no")}, nil},
		{"a key with no row", []repoSetting{set("core.fsmonitor", "true"), set("core.sshcommand", "x")}, nil},
		{"a key with no value (not measured)", []repoSetting{{key: "core.filemode"}}, []string{"-c", "core.filemode=true"}},
		{"a value that fails keeps the one before it (not measured)",
			[]repoSetting{set("core.filemode", "false"), set("core.filemode", "maybe")}, []string{"-c", "core.filemode=false"}},
		{"a key with a subsection (not measured)", []repoSetting{set("remote.origin.url", "https://example.invalid/o/r")}, nil},
		{"the format version and an extension", []repoSetting{set("core.repositoryformatversion", "1"), set("extensions.noop", "true")}, nil},
		// Measured on Linux: rewritten values, one item per key, sorted.
		{"C3a", []repoSetting{set("core.quotepath", "off"), set("core.filemode", "no"), set("core.trustctime", "false"), set("core.bare", "true"), set("core.bare", "false")},
			[]string{"-c", "core.filemode=false", "-c", "core.quotepath=false", "-c", "core.trustctime=false"}},
		{"no file", nil, nil},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := worktreeConfigItems(row.settings); !slices.Equal(got, row.want) {
				t.Errorf("items = %q, want %q", got, row.want)
			}
		})
	}
}

// TestWithFront: the items come before the list, and the list of the caller is
// not changed.
func TestWithFront(t *testing.T) {
	front := []string{"-c", "core.eol=lf"}
	argv := []string{"-c", "core.fsmonitor=", "status"}
	got := withFront(front, argv)
	if want := []string{"-c", "core.eol=lf", "-c", "core.fsmonitor=", "status"}; !slices.Equal(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
	if len(front) != 2 || front[1] != "core.eol=lf" {
		t.Errorf("the items of the request changed: %q", front)
	}
	if got := withFront(nil, argv); !slices.Equal(got, argv) {
		t.Errorf("with no item argv = %q, want %q", got, argv)
	}
}
