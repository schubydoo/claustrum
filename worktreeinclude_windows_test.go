package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestIncludeWindowsConstants pins the Windows argv budget. It is fitted to
// the f6010b97 batch counts of both passes.
func TestIncludeWindowsConstants(t *testing.T) {
	if gitArgvBudget != 24576 {
		t.Errorf("gitArgvBudget = %d, want 24576 (fitted to the Windows batch counts)", gitArgvBudget)
	}
}

// TestPlanIncludeScanBatchLayoutWindows replays the f6010b97 batch sizes that a
// Windows VM measured. Each row uses the length of the `--exclude-from`
// argument that its run had. It was 85 to 87 bytes with the default temp
// directory, and 120 bytes with a longer TMP. The Bb and BbT rows are the one-byte
// bisection: a batch of 24 576 bytes is one call, and 24 577 bytes splits.
func TestPlanIncludeScanBatchLayoutWindows(t *testing.T) {
	names := func(n int, format string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf(format, i)
		}
		return out
	}
	listing := func(files, dirs []string) string {
		var ents []string
		for _, d := range dirs {
			ents = append(ents, d+"/")
		}
		ents = append(ents, files...)
		slices.Sort(ents)
		return ".claude/\x00.claude/worktrees/\x00" + strings.Join(ents, "\x00") + "\x00"
	}
	sizes := func(bs [][]string) []int {
		out := []int{}
		for _, b := range bs {
			out = append(out, len(b))
		}
		return out
	}
	// bisect is bisectDirs of the probe: n dirs of 12 bytes, then one dir of
	// l bytes that sorts after them.
	bisect := func(n, l int) []string {
		return append(names(n, "d%011d"), "e"+strings.Repeat("x", l-1))
	}
	dirs := func(n int) []string { return names(n, "d%011d") }
	pads := names(4000, "P%06d"+strings.Repeat("x", 30)+".pad")
	mpads := append(names(10180, "P%06d"+strings.Repeat("x", 89)+".pad"), "Q000000"+strings.Repeat("x", 22)+".pad")
	repeat := func(v, n, last int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = v
		}
		return append(out, last)
	}
	for _, c := range []struct {
		name         string
		argLen       int
		manifest     string
		files, dirs  []string
		wantF, wantD []int
	}{
		{"Bt_dirs2500", 87, "d*/\n", nil, dirs(2500), []int{}, []int{1628, 872}},
		{"Bt_dirs9000", 86, "d*/\n", nil, dirs(9000), []int{}, repeat(1628, 5, 860)},
		{"Bt_files8000_dirs9000", 87, "d*/\n", names(8000, "f%07d.pd"), dirs(9000), repeat(1744, 4, 1024), repeat(1628, 5, 860)},
		{"Em_ctl", 86, "build/\nP0000*\nP0039*\n", pads, []string{"build"}, repeat(555, 7, 115), []int{1}},
		{"M_1048576", 87, "*.txt\n", mpads, []string{"build"}, repeat(237, 42, 227), []int{}},
		{"Bb_l12", 85, "d*/\ne*/\n", nil, bisect(1627, 12), []int{}, []int{1628}},
		{"Bb_l13", 87, "d*/\ne*/\n", nil, bisect(1627, 13), []int{}, []int{1627, 1}},
		{"BbT_l09", 120, "d*/\ne*/\n", nil, bisect(1625, 9), []int{}, []int{1626}},
		{"BbT_l10", 120, "d*/\ne*/\n", nil, bisect(1625, 10), []int{}, []int{1625, 1}},
		{"BbT_dirs2500", 120, "d*/\n", nil, dirs(2500), []int{}, []int{1625, 875}},
	} {
		arg := "--exclude-from=" + strings.Repeat("t", c.argLen-len("--exclude-from="))
		p := planIncludeScan([]byte(c.manifest), listing(c.files, c.dirs), arg)
		if gotF, gotD := sizes(p.fileBatches), sizes(p.dirBatches); p.fallback || !slices.Equal(gotF, c.wantF) || !slices.Equal(gotD, c.wantD) {
			t.Errorf("%s: fallback %v, file batches %v, dir batches %v, want %v and %v (f6010b97)",
				c.name, p.fallback, gotF, gotD, c.wantF, c.wantD)
		}
	}
}

// TestIncludeBackslashOpeningWindows runs the Windows backslash rows end to end
// with the yTree fixture of the Windows probe. The want lists are the f6010b97
// file sets.
func TestIncludeBackslashOpeningWindows(t *testing.T) {
	y := func(name, manifest string, want ...string) includeCase {
		return includeCase{name: name, ignore: bld3Ignore, files: bld3Files, manifest: manifest, want: want}
	}
	checkIncludeCases(t, []includeCase{
		y("O2_anch", "/build/\n*.txt\n", ".bx/d.txt", "build/a.txt"),
		y("O2_bslash_sep", "sub\\build/\n*.txt\n", ".bx/d.txt", "sub/build/b.txt"),
		y("O3_sub_esc_b_star", "sub\\\\build/\n*.txt\n", ".bx/d.txt", "sub/build/b.txt"),
		y("O2_bslash_trail", "build\\\n*.txt\n", ".bx/d.txt", "build/a.txt"),
		y("O2_bslash_glob", "sub\\b*/\n*.txt\n", ".bx/d.txt", "sub/build/b.txt"),
		y("O2_bslash_file", "build\\a.txt\n*.txt\n", ".bx/d.txt", "build/a.txt"),
		y("O2_bslash_axbuild", "a\\x\\build/\n*.txt\n", ".bx/d.txt", "a/x/build/c.txt"),
		y("O2_bslash_lead", "\\build/\n*.txt\n", ".bx/d.txt"),
		y("O2_bslash_ds", "**\\build/\n*.txt\n", ".bx/d.txt"),
		y("O3_bu_esc_ild", "bu\\ild/\n*.txt\n", ".bx/d.txt", "build/a.txt"),
		{name: "O3_weird_esc_txt", ignore: lines("we[i]rd/", "weird/"), files: []string{"we[i]rd/a.txt", "weird/b.txt"},
			manifest: "we\\[i\\]rd/\n*.txt\n", want: []string{"weird/b.txt"}},
	})
}
