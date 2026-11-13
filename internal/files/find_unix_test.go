//go:build unix

package files

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// findTree makes the files under root, each as "x". A name that ends with "/"
// is a folder.
func findTree(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(root, n)
		if strings.HasSuffix(n, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// findLink makes the link at root/name with the target as given.
func findLink(t *testing.T, root, name, target string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// findCall answers one files.find with the params as given, as a frame with the
// root tokenized to <R>.
func findCall(t *testing.T, root string, params any) string {
	t.Helper()
	got := dispatchRaw(t, nil, rpcLine(t, "files.find", params))
	if root != "" {
		got = strings.ReplaceAll(got, root, "<R>")
	}
	return got
}

// findResult is the result of one files.find with the params as given.
func findResult(t *testing.T, params map[string]any) rpc.FindResult {
	t.Helper()
	var resp struct {
		Result *rpc.FindResult `json:"result"`
		Error  any             `json:"error"`
	}
	frame := findCall(t, "", params)
	if err := json.Unmarshal([]byte(frame), &resp); err != nil || resp.Result == nil {
		t.Fatalf("frame %s: no result (%v)", frame, err)
	}
	return *resp.Result
}

const findInvalid = `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`

// TestFindRefusals: each shape that 4c111e86 refuses gets -32602, and the shapes
// at the bounds pass (measured on Linux and macOS).
func TestFindRefusals(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	findTree(t, root, "a/")
	nine := make([]string, 9)
	for i := range nine {
		nine[i] = root
	}
	s := ".claude/settings.json"
	for _, p := range []any{
		map[string]any{"roots": []string{}, "suffix": s},
		map[string]any{"suffix": s},
		map[string]any{"roots": nine, "suffix": s},
		map[string]any{"roots": []string{"a/b"}, "suffix": s},
		map[string]any{"roots": []string{"."}, "suffix": s},
		map[string]any{"roots": []string{root + "/a/../a"}, "suffix": s},
		map[string]any{"roots": []string{root + "/"}, "suffix": s},
		map[string]any{"roots": []string{root + "/./a"}, "suffix": s},
		map[string]any{"roots": []string{root + "//a"}, "suffix": s},
		map[string]any{"roots": []string{root + "\x00/a"}, "suffix": s},
		map[string]any{"roots": []string{""}, "suffix": s},
		map[string]any{"roots": []string{root, "rel"}, "suffix": s},
		map[string]any{"roots": []string{root}, "suffix": "a\x00b"},
		map[string]any{"roots": []string{root}, "suffix": "../a"},
		map[string]any{"roots": []string{root}, "suffix": "./a"},
		map[string]any{"roots": []string{root}, "suffix": "a//b"},
		map[string]any{"roots": []string{root}, "suffix": "/a"},
		map[string]any{"roots": []string{root}, "suffix": "a/"},
		map[string]any{"roots": []string{root}, "suffix": ""},
		map[string]any{"roots": []string{root}},
		map[string]any{"roots": []string{root}, "suffix": "a/b/c/d/e/f/g/h/i"},
		map[string]any{"roots": []string{root}, "suffix": s, "limit": -1},
		map[string]any{"roots": []string{root}, "suffix": s, "timeoutMs": -1},
		map[string]any{"roots": []string{root}, "suffix": s, "limit": 1.5},
		map[string]any{"roots": []string{root}, "suffix": s, "limit": "3"},
		map[string]any{"roots": root, "suffix": s},
		map[string]any{"roots": []any{1}, "suffix": s},
		map[string]any{"roots": []string{root}, "suffix": 5},
		[]int{1},
	} {
		if got := findCall(t, root, p); got != findInvalid {
			t.Errorf("%v = %s, want -32602", p, got)
		}
	}
	whole := `{"jsonrpc":"2.0","id":1,"result":{"paths":[],"whole":true,"passedOver":[],"links":[]}}`
	eight := nine[:8]
	for _, p := range []map[string]any{
		{"roots": eight, "suffix": s},
		{"roots": []string{root}, "suffix": "a/b/c/d/e/f/g/h"},
		{"roots": []string{root}, "suffix": s, "bogus": 1},
		{"roots": []string{root}, "suffix": s, "limit": 0, "timeoutMs": 0},
		{"roots": []string{root}, "suffix": s, "limit": 1000, "timeoutMs": 9000},
		{"roots": []string{"/"}, "suffix": "nothing.here", "timeoutMs": 1, "limit": 1},
	} {
		got := findCall(t, root, p)
		if strings.Contains(got, `"error"`) || (p["roots"].([]string)[0] != "/" && got != whole) {
			t.Errorf("%v = %s, want %s", p, got, whole)
		}
	}
}

// TestFindNameCut: a suffix with a rune that has no safe display gets the cut
// "name" and no walk (4c111e86, measured on Linux and macOS).
func TestFindNameCut(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	findTree(t, root, "set\ttings.json")
	want := `{"jsonrpc":"2.0","id":1,"result":{"paths":[],"whole":false,"cut":"name","passedOver":[],"links":[]}}`
	for _, sfx := range []string{"set\x01tings.json", "set\ntings.json", "set\ttings.json", "set\u202etings.json",
		"set\u200ftings.json", "set\u2028tings.json", "settings.json\u2800", "\u2800settings.json", "settings.json ",
		" settings.json", "set\ufffdtings.json", "set\x7ftings.json", "set\u0085tings.json", "a/ ", "a/\u2800"} {
		if got := findCall(t, root, map[string]any{"roots": []string{root}, "suffix": sfx}); got != want {
			t.Errorf("suffix %q = %s, want the cut name", sfx, got)
		}
	}
	ok := `{"jsonrpc":"2.0","id":1,"result":{"paths":[],"whole":true,"passedOver":[],"links":[]}}`
	for _, sfx := range []string{"sett\u00efngs.json", "a/\u00e9"} {
		if got := findCall(t, root, map[string]any{"roots": []string{root}, "suffix": sfx}); got != ok {
			t.Errorf("suffix %q = %s, want a whole walk", sfx, got)
		}
	}
	// A lone surrogate in the JSON text decodes to U+FFFD and gets "name".
	line := `{"jsonrpc":"2.0","id":1,"method":"files.find","params":{"roots":["` + root + `"],"suffix":"set\ud800tings.json"}}`
	if got := dispatchRaw(t, nil, line); got != want {
		t.Errorf("lone surrogate = %s, want the cut name", got)
	}
}

// TestFindHitsAndLimit: the default limit is 5, the cap is 20, a hit past the
// limit gives "count", a path met twice counts once, and the paths sort by the
// number of parts, then by bytes (4c111e86, measured on Linux and macOS).
func TestFindHitsAndLimit(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	a := filepath.Join(root, "A")
	var names []string
	for i := range 22 {
		names = append(names, fmt.Sprintf("d%02d%s/.claude/settings.json", i, strings.Repeat("/x", i%4)))
	}
	findTree(t, a, names...)
	findTree(t, root, "B/b0/.claude/settings.json", "B/b1/.claude/settings.json", "TW/up/.claude/SETTINGS.JSON",
		"TW/up/.claude/mysettings.json", "flat/settings.json")
	sfx := ".claude/settings.json"
	for _, c := range []struct {
		limit any
		n     int
	}{{nil, 5}, {0, 5}, {1, 1}, {3, 3}, {19, 19}, {20, 20}, {21, 20}, {1000, 20}} {
		p := map[string]any{"roots": []string{a}, "suffix": sfx}
		if c.limit != nil {
			p["limit"] = c.limit
		}
		res := findResult(t, p)
		if len(res.Paths) != c.n || res.Whole || res.Cut != "count" {
			t.Errorf("limit %v: %d paths, whole %v, cut %q, want %d and count", c.limit, len(res.Paths), res.Whole, res.Cut, c.n)
		}
		for i := 1; i < len(res.Paths); i++ {
			if !findLess(res.Paths[i-1], res.Paths[i]) {
				t.Errorf("limit %v: %s before %s", c.limit, res.Paths[i-1], res.Paths[i])
			}
		}
	}
	// The same root twice and a root inside another count each path once.
	b := filepath.Join(root, "B")
	res := findResult(t, map[string]any{"roots": []string{b, b, b + "/b0"}, "suffix": sfx})
	want := []string{b + "/b0/.claude/settings.json", b + "/b1/.claude/settings.json"}
	if !res.Whole || strings.Join(res.Paths, " ") != strings.Join(want, " ") {
		t.Errorf("twice: %+v, want whole %v", res, want)
	}
	// A walk that finds exactly the limit stays whole (claustrum's own).
	if res := findResult(t, map[string]any{"roots": []string{b}, "suffix": sfx, "limit": 2}); !res.Whole || len(res.Paths) != 2 {
		t.Errorf("exact limit: %+v, want whole with 2 paths", res)
	}
	// The order: fewer parts first, then bytes.
	c := filepath.Join(root, "C")
	findTree(t, c, "a/x/.claude/settings.json", "z/.claude/settings.json", "b/.claude/settings.json")
	res = findResult(t, map[string]any{"roots": []string{c}, "suffix": sfx})
	want = []string{c + "/b/.claude/settings.json", c + "/z/.claude/settings.json", c + "/a/x/.claude/settings.json"}
	if strings.Join(res.Paths, " ") != strings.Join(want, " ") {
		t.Errorf("order: %v, want %v", res.Paths, want)
	}
	// Names compare by bytes: a case twin and a part of a name do not match.
	for sfx, n := range map[string]int{"SETTINGS.JSON": 1, "tings.json": 0, "settings.json": 1} {
		res := findResult(t, map[string]any{"roots": []string{filepath.Join(root, "TW"), filepath.Join(root, "flat")}, "suffix": sfx})
		if len(res.Paths) != n || !res.Whole {
			t.Errorf("suffix %s: %+v, want %d paths", sfx, res, n)
		}
	}
}

// TestFindPassOver: a folder .git, .hg, .svn or node_modules, and a folder with a
// regular file pyvenv.cfg, is listed in passedOver and not walked. A file .git, a
// folder pyvenv.cfg and the root itself are walked (4c111e86, measured on Linux
// and macOS).
func TestFindPassOver(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	findTree(t, root, ".git/t.txt", ".hg/t.txt", ".svn/t.txt", "a/node_modules/b/t.txt", "a/ok/t.txt",
		"v/pyvenv.cfg", "v/lib/t.txt", "plain/lib/t.txt", "cfgdir/pyvenv.cfg/x", "gitfile/.git", "gitfile/t.txt")
	res := findResult(t, map[string]any{"roots": []string{root}, "suffix": "t.txt", "limit": 20})
	wantPaths := []string{root + "/gitfile/t.txt", root + "/a/ok/t.txt", root + "/plain/lib/t.txt"}
	wantPassed := []string{root + "/.git", root + "/.hg", root + "/.svn", root + "/v", root + "/a/node_modules"}
	if !res.Whole || strings.Join(res.Paths, " ") != strings.Join(wantPaths, " ") ||
		strings.Join(res.PassedOver, " ") != strings.Join(wantPassed, " ") {
		t.Errorf("got %+v, want paths %v and passedOver %v", res, wantPaths, wantPassed)
	}
	res = findResult(t, map[string]any{"roots": []string{root + "/.git", root + "/a/node_modules", root + "/v"}, "suffix": "t.txt"})
	if !res.Whole || len(res.Paths) != 3 || len(res.PassedOver) != 0 {
		t.Errorf("roots that pass: %+v, want 3 paths and no pass", res)
	}
}

// TestFindLinks: a link is never followed. An absolute target leads to "other",
// a relative target in the root leads to "folder" for a folder and is left out
// for a regular file, and any other target leads to "other". A matching link
// gives not_a_file, links alone give "links", and the list stops at 20
// (4c111e86, measured on Linux; an absolute target on macOS too).
func TestFindLinks(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	findTree(t, root, "real/.claude/settings.json", "rel/a/real.txt", "plain/b/t.txt", "d/sub/")
	other := filepath.Join(t.TempDir(), "o")
	findTree(t, other, "t.txt")
	findLink(t, root, "lk/.claude/settings.json", root+"/real/.claude/settings.json")
	findLink(t, root, "ldir/.claude", root+"/real")
	res := findResult(t, map[string]any{"roots": []string{root + "/lk", root + "/ldir"}, "suffix": ".claude/settings.json"})
	want := `[{ldir/.claude other} {lk/.claude/settings.json other}]`
	if got := strings.ReplaceAll(fmt.Sprint(res.Links), root+"/", ""); got != want || res.Cut != "not_a_file" || len(res.Paths) != 0 {
		t.Errorf("absolute: %+v, want links %s and not_a_file", res, want)
	}
	findLink(t, root, "rel/a/t.txt", "real.txt")
	res = findResult(t, map[string]any{"roots": []string{root + "/rel"}, "suffix": "t.txt"})
	if len(res.Links) != 0 || res.Cut != "not_a_file" {
		t.Errorf("relative file: %+v, want no link and not_a_file", res)
	}
	findLink(t, root, "d/up", "sub")
	findLink(t, root, "d/out", "../../"+filepath.Base(filepath.Dir(other))+"/o")
	findLink(t, root, "d/self", "self")
	findLink(t, root, "d/gone", "nothing")
	findLink(t, root, "d/abs", "/sub")
	res = findResult(t, map[string]any{"roots": []string{root + "/d"}, "suffix": "t.txt"})
	want = `[{d/abs other} {d/gone other} {d/out other} {d/self other} {d/up folder}]`
	if got := strings.ReplaceAll(fmt.Sprint(res.Links), root+"/", ""); got != want || res.Cut != "links" || res.Whole {
		t.Errorf("relative: %+v, want links %s and the cut links", res, want)
	}
	// A matching link and a plain hit: the hit is listed, the cut is not_a_file.
	findLink(t, root, "plain/a/t.txt", other+"/t.txt")
	res = findResult(t, map[string]any{"roots": []string{root + "/plain"}, "suffix": "t.txt"})
	if len(res.Paths) != 1 || len(res.Links) != 1 || res.Links[0].LeadsTo != "other" || res.Cut != "not_a_file" {
		t.Errorf("link and hit: %+v", res)
	}
	many := filepath.Join(root, "many")
	for i := range 30 {
		findLink(t, many, fmt.Sprintf("m%03d/t.txt", i), "/etc/hostname")
	}
	for _, limit := range []int{1, 20} {
		res = findResult(t, map[string]any{"roots": []string{many}, "suffix": "t.txt", "limit": limit})
		if len(res.Links) != 20 || res.Cut != "not_a_file" || len(res.Paths) != 0 {
			t.Errorf("30 links, limit %d: %d links, cut %q, want 20 and not_a_file", limit, len(res.Links), res.Cut)
		}
	}
	// Links that do not match: 20 give "links", 21 give "many_links", and a hit
	// after the links is still listed (4c111e86, measured on Linux).
	capped := filepath.Join(root, "capped")
	for i := range 21 {
		findLink(t, capped, fmt.Sprintf("a/l%02dhit.txt", i), "/etc/hostname")
	}
	findTree(t, capped, "z/deep/hit.txt")
	if err := os.Remove(filepath.Join(capped, "a/l20hit.txt")); err != nil {
		t.Fatal(err)
	}
	res = findResult(t, map[string]any{"roots": []string{capped}, "suffix": "hit.txt"})
	if len(res.Links) != 20 || res.Cut != "links" || len(res.Paths) != 1 {
		t.Errorf("20 links: %d links, cut %q, %d paths, want 20, links and 1", len(res.Links), res.Cut, len(res.Paths))
	}
	findLink(t, capped, "a/l20hit.txt", "/etc/hostname")
	res = findResult(t, map[string]any{"roots": []string{capped}, "suffix": "hit.txt"})
	if len(res.Links) != 20 || res.Cut != "many_links" || res.Whole || len(res.Paths) != 1 {
		t.Errorf("21 links: %+v, want 20 links, many_links and the hit", res)
	}
}

// TestFindCuts: the cut words of one walk (4c111e86, measured on Linux).
func TestFindCuts(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	chain := filepath.Join(root, "chain")
	p := chain
	for k := 1; k <= 14; k++ {
		p += fmt.Sprintf("/c%d", k)
		findTree(t, p, fmt.Sprintf("t%d.txt", k))
	}
	findTree(t, root, "dirhit/.claude/settings.json/", "file.txt", "big/aa/f0", "big/aa/f1", "big/aa/f2", "big/zz/t.txt")
	findTree(t, root, "fifo/")
	if err := syscall.Mkfifo(filepath.Join(root, "fifo", "t.txt"), 0o644); err != nil {
		t.Fatal(err)
	}
	findLink(t, root, "lnroot", "chain")
	cut := func(params map[string]any) (string, int) {
		res := findResult(t, params)
		if res.Whole != (res.Cut == "") {
			t.Errorf("%v: whole %v with cut %q", params, res.Whole, res.Cut)
		}
		return res.Cut, len(res.Paths)
	}
	for _, c := range []struct {
		name   string
		params map[string]any
		cut    string
		n      int
	}{
		{"hit at 12", map[string]any{"roots": []string{chain}, "suffix": "t12.txt"}, "depth", 1},
		{"hit at 13", map[string]any{"roots": []string{chain}, "suffix": "t13.txt"}, "depth", 0},
		{"deeper root", map[string]any{"roots": []string{chain + "/c1/c2"}, "suffix": "t14.txt"}, "", 1},
		{"missing root", map[string]any{"roots": []string{root + "/nonesuch"}, "suffix": "t.txt"}, "root", 0},
		{"file root", map[string]any{"roots": []string{root + "/file.txt"}, "suffix": "t.txt"}, "root", 0},
		{"link root", map[string]any{"roots": []string{root + "/lnroot"}, "suffix": "t1.txt"}, "root", 0},
		{"link root and a good one", map[string]any{"roots": []string{root + "/lnroot", chain}, "suffix": "t1.txt"}, "root", 1},
		{"missing root and a good one", map[string]any{"roots": []string{root + "/nonesuch", chain}, "suffix": "t1.txt"}, "root", 1},
		{"folder hit", map[string]any{"roots": []string{root + "/dirhit"}, "suffix": ".claude/settings.json"}, "not_a_file", 0},
		{"fifo hit", map[string]any{"roots": []string{root + "/fifo"}, "suffix": "t.txt"}, "not_a_file", 0},
	} {
		if got, n := cut(c.params); got != c.cut || n != c.n {
			t.Errorf("%s: cut %q with %d paths, want %q with %d", c.name, got, n, c.cut, c.n)
		}
	}

	// The entry bound ends the walk with "entries".
	old := findMaxEntries
	findMaxEntries = 4
	got, _ := cut(map[string]any{"roots": []string{root + "/big"}, "suffix": "t.txt"})
	findMaxEntries = old
	if got != "entries" {
		t.Errorf("entry bound: cut %q, want entries", got)
	}

	// The rows put the entry bound between 49000 and 51000 (measured on Linux).
	if findMaxEntries < 49000 || findMaxEntries >= 51000 {
		t.Errorf("entry bound %d, want 49000 to 50999", findMaxEntries)
	}

	// With no timeoutMs or 0 the bound is 1.5 s, and 10000 gives 5 s (4c111e86,
	// measured on Linux). 9000 and 1e13 give 5 s too (claustrum's own).
	for _, c := range []struct {
		timeoutMs any
		after     time.Duration
		cut       string
	}{{nil, 1499 * time.Millisecond, ""}, {nil, 1501 * time.Millisecond, "time"},
		{10000, 4999 * time.Millisecond, ""}, {10000, 5001 * time.Millisecond, "time"},
		{9000, 4999 * time.Millisecond, ""}, {9000, 5001 * time.Millisecond, "time"},
		{int64(1e13), 4999 * time.Millisecond, ""}, {int64(1e13), 5001 * time.Millisecond, "time"}} {
		start := findNow()
		first := true
		findNow = func() time.Time {
			if first {
				first = false
				return start
			}
			return start.Add(c.after)
		}
		params := map[string]any{"roots": []string{root + "/big"}, "suffix": "t.txt"}
		if c.timeoutMs != nil {
			params["timeoutMs"] = c.timeoutMs
		}
		got, _ = cut(params)
		findNow = time.Now
		if got != c.cut {
			t.Errorf("timeoutMs %v at %v: cut %q, want %q", c.timeoutMs, c.after, got, c.cut)
		}
	}

	// The time bound ends the walk with "time".
	oldNow := findNow
	stamp := oldNow()
	calls := 0
	findNow = func() time.Time {
		calls++
		if calls == 1 {
			return stamp
		}
		return stamp.Add(2 * time.Millisecond)
	}
	got, _ = cut(map[string]any{"roots": []string{root + "/big"}, "suffix": "t.txt", "timeoutMs": 1})
	findNow = oldNow
	if got != "time" {
		t.Errorf("time bound: cut %q, want time", got)
	}

	// With 4 walks running, a fifth gets "busy" at once.
	if cap(findSlots) != 4 {
		t.Errorf("walks at once: %d, want 4", cap(findSlots))
	}
	for range findMaxWalks {
		findSlots <- struct{}{}
	}
	got, _ = cut(map[string]any{"roots": []string{chain}, "suffix": "t1.txt"})
	for range findMaxWalks {
		<-findSlots
	}
	if got != "busy" {
		t.Errorf("4 walks at once: cut %q, want busy", got)
	}
	if got, n := cut(map[string]any{"roots": []string{chain}, "suffix": "t3.txt"}); got != "depth" || n != 1 {
		t.Errorf("after busy: cut %q with %d paths, want depth with 1", got, n)
	}
}

// TestFindOverlapOnce: with a root inside another root, each link and each
// passed folder is listed once (4c111e86, measured on Linux and macOS).
func TestFindOverlapOnce(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	findTree(t, root, "sub/.git/t.txt", "sub/t.txt")
	findLink(t, root, "sub/l", "/etc/hostname")
	res := findResult(t, map[string]any{"roots": []string{root, root + "/sub"}, "suffix": "t.txt"})
	if len(res.Links) != 1 || len(res.PassedOver) != 1 || len(res.Paths) != 1 {
		t.Errorf("overlap: %+v, want 1 link, 1 passed folder and 1 path", res)
	}
}

// TestFindUnreadable: a folder that does not open gives "unreadable", and the
// walk goes on (4c111e86, measured on Linux).
func TestFindUnreadable(t *testing.T) {
	skipIfRoot(t)
	root := resolveTestRoot(t, t.TempDir())
	findTree(t, root, "locked/t.txt", "ok/t.txt")
	if err := os.Chmod(filepath.Join(root, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "locked"), 0o755) })
	res := findResult(t, map[string]any{"roots": []string{root}, "suffix": "t.txt", "limit": 20})
	if res.Cut != "unreadable" || len(res.Paths) != 1 || res.Paths[0] != root+"/ok/t.txt" {
		t.Errorf("got %+v, want ok/t.txt and unreadable", res)
	}
}

// TestFindMacRootCut: on macOS a root below /System/Volumes/Data gets "root",
// and the data volume, home and each folder above home get "home" (4c111e86,
// measured on macOS).
func TestFindMacRootCut(t *testing.T) {
	home := "/Users/u"
	holds := func(p string) bool { return p == "/" || p == home || strings.HasPrefix(home, p+"/") }
	for root, want := range map[string]string{
		"/":                                 "home",
		"/Users":                            "home",
		home:                                "home",
		home + "/sub":                       "",
		"/private/tmp":                      "",
		"/System/Volumes/Data":              "home",
		"/System/Volumes/Data/Users/u":      "root",
		"/System/Volumes/Data/Users/u/sub":  "root",
		"/System/Volumes/Data/private":      "root",
		"/System/Volumes/DataX":             "",
		"/System/Volumes/Data/Users/u/Libr": "root",
	} {
		if got := findMacRootCut(root, holds); got != want {
			t.Errorf("%s: %q, want %q", root, got, want)
		}
	}
}

// TestFindChanged: a folder whose entries change while the walk reads it gives
// the cut "changed" (4c111e86, measured on Linux). The test by the stamps of the
// open folder is claustrum's own.
func TestFindChanged(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	findTree(t, root, "a/f0", "a/f1", "b/t.txt")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(root, "a"), old, old); err != nil {
		t.Fatal(err)
	}
	read := findReadDir
	t.Cleanup(func() { findReadDir = read })
	findReadDir = func(f *os.File, n int) ([]os.DirEntry, error) {
		if f.Name() == root+"/a" {
			if err := os.WriteFile(filepath.Join(root, "a", "new"), nil, 0o644); err != nil {
				t.Error(err)
			}
		}
		return read(f, n)
	}
	res := findResult(t, map[string]any{"roots": []string{root}, "suffix": "t.txt"})
	if res.Whole || res.Cut != "changed" || len(res.Paths) != 1 {
		t.Errorf("changed folder: %+v, want the cut changed and the hit", res)
	}
	findReadDir = read
	res = findResult(t, map[string]any{"roots": []string{root}, "suffix": "t.txt"})
	if !res.Whole || len(res.Paths) != 1 {
		t.Errorf("still folder: %+v, want whole", res)
	}
}

// findSlotsFree waits until no walk holds a slot, with a limit.
func findSlotsFree(t *testing.T) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); len(findSlots) != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%d walk slots still held", len(findSlots))
		}
	}
}

// TestFindStalledRead: a folder read that blocks past the bound gives the reply
// at the bound with the cut "time", and the stalled walk keeps its slot until
// its read ends (4c111e86, measured on Linux).
func TestFindStalledRead(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	findTree(t, root, "stall/t.txt", "fast/t.txt")
	findSlotsFree(t)
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	read := findReadDir
	findReadDir = func(f *os.File, n int) ([]os.DirEntry, error) {
		if f.Name() == root+"/stall" {
			<-release
		}
		return read(f, n)
	}
	t.Cleanup(func() {
		free()
		findSlotsFree(t)
		findReadDir = read
	})
	const bound = 300 * time.Millisecond
	call := func(sub string) (rpc.FindResult, time.Duration) {
		t.Helper()
		start := time.Now()
		got := make(chan rpc.FindResult, 1)
		go func() {
			got <- findRun(&findParams{Roots: []string{root + "/" + sub}, Suffix: "t.txt", TimeoutMs: bound.Milliseconds()})
		}()
		select {
		case res := <-got:
			return res, time.Since(start)
		case <-time.After(3 * time.Second):
			free()
			<-got
			t.Fatalf("%s: no reply after 3 s", sub)
			return rpc.FindResult{}, 0
		}
	}
	for i := 1; i <= findMaxWalks; i++ {
		res, took := call("stall")
		if res.Whole || res.Cut != "time" || len(res.Paths) != 0 || took < bound || took > 2*time.Second {
			t.Errorf("stalled walk %d: %+v after %v, want the cut time at %v", i, res, took, bound)
		}
		if len(findSlots) != i {
			t.Errorf("stalled walk %d: %d slots held, want %d", i, len(findSlots), i)
		}
		res, _ = call("fast")
		want := i < findMaxWalks
		if (res.Cut == "") != want || (res.Cut == "busy") == want {
			t.Errorf("fast walk with %d held: %+v, want busy %v", i, res, !want)
		}
	}
	free()
	findSlotsFree(t)
	if res, _ := call("fast"); !res.Whole || len(res.Paths) != 1 {
		t.Errorf("after the release: %+v, want whole with 1 path", res)
	}
}

// TestFindWalkPanic: a panic in the walk does not end the daemon. The call
// replies with the cut "unreadable" and one log line, and the slot frees
// (claustrum's own, not measured).
func TestFindWalkPanic(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	findTree(t, root, "boom/t.txt")
	findSlotsFree(t)
	logs := captureLogBuf(t)
	read := findReadDir
	t.Cleanup(func() { findReadDir = read })
	findReadDir = func(f *os.File, n int) ([]os.DirEntry, error) {
		if f.Name() == root+"/boom" {
			panic("find walk test panic")
		}
		return read(f, n)
	}
	got := make(chan rpc.FindResult, 1)
	go func() {
		got <- findRun(&findParams{Roots: []string{root + "/boom"}, Suffix: "t.txt", TimeoutMs: 5000})
	}()
	select {
	case res := <-got:
		if res.Whole || res.Cut != "unreadable" || len(res.Paths) != 0 {
			t.Errorf("panic: %+v, want the cut unreadable", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("panic: no reply after 3 s")
	}
	findSlotsFree(t)
	if want := "[Files] find: recovered panic in the walk: find walk test panic"; strings.Count(logs.String(), want) != 1 {
		t.Errorf("log %q, want one line %q", logs.String(), want)
	}
}

// TestFindJoinSlashRoot: the root "/" joins a name with one "/" (4c111e86,
// measured on Linux). Another folder joins with one "/" too.
func TestFindJoinSlashRoot(t *testing.T) {
	for _, c := range [][3]string{{"/", "x", "/x"}, {"/a/b", "x", "/a/b/x"}} {
		if got := findJoin(c[0], c[1]); got != c[2] {
			t.Errorf("findJoin(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
