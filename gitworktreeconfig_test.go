package main

import (
	"slices"
	"testing"
)

// TestWorktreeConfigItems pins which settings of a `config.worktree` file go to
// the git calls as `-c` items, and their form. 5fd08069 sends
// `core.filemode=false` and `core.sparsecheckout=true` (cells 12a to 12c and
// 12f of val-s3p2b2-3-linux, Linux VM). It sends `core.filemode=true` (cell
// N16c of val-s3p2b2-3-windows, Windows VM) and `core.eol=lf` (cell 1c of
// val-s3p2b2-3-linux). It sends no item for `core.bare` and `core.worktree`
// (cells 12a, 12d, 12e and 9a). The six rows p2-K are the cells of
// val-s3p2b2-6-linux with those names (Linux VM). Row U1 of val-s3p2b2-6-macos
// has the four keys with an item on a macOS VM. Four keys give an item, and
// `core.hookspath` and `user.name` give none. Every other key has no row and
// gives no item.
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
		{"the order of the file", []repoSetting{set("core.sparsecheckout", "true"), set("core.filemode", "false")},
			[]string{"-c", "core.sparsecheckout=true", "-c", "core.filemode=false"}},
		{"p2-K-autocrlf-input", []repoSetting{set("core.autocrlf", "input")}, []string{"-c", "core.autocrlf=input"}},
		{"p2-K-ignorecase-true", []repoSetting{set("core.ignorecase", "true")}, []string{"-c", "core.ignorecase=true"}},
		{"p2-K-symlinks-false", []repoSetting{set("core.symlinks", "false")}, []string{"-c", "core.symlinks=false"}},
		{"p2-K-sparsecheckoutcone-true", []repoSetting{set("core.sparsecheckoutcone", "true")}, []string{"-c", "core.sparsecheckoutcone=true"}},
		{"p2-K-hookspath-x", []repoSetting{set("core.hookspath", "/x")}, nil},
		{"p2-K-user-name-n", []repoSetting{set("user.name", "n")}, nil},
		{"a key with no row", []repoSetting{set("core.fsmonitor", "true"), set("core.sshcommand", "x")}, nil},
		{"a key with no value (not measured)", []repoSetting{{key: "core.filemode"}}, nil},
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
	argv := []string{"-c", "core.fsmonitor=false", "status"}
	got := withFront(front, argv)
	if want := []string{"-c", "core.eol=lf", "-c", "core.fsmonitor=false", "status"}; !slices.Equal(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
	if len(front) != 2 || front[1] != "core.eol=lf" {
		t.Errorf("the items of the request changed: %q", front)
	}
	if got := withFront(nil, argv); !slices.Equal(got, argv) {
		t.Errorf("with no item argv = %q, want %q", got, argv)
	}
}
