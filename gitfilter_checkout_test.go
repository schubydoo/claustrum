package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The rules of round task008-01 for the checkout of git.worktree_create in the
// state shadow. They are the cap on the filter names of the repository, the
// second listing, the lfs.storage item and a filter that both configs define.
// The git stub stands in for the wrapper of the round.

// checkoutRuleFixture is a seeded shadow fixture with the paths of one create.
type checkoutRuleFixture struct {
	shadowFixture
	leaf, reg string
}

func newCheckoutRuleFixture(t *testing.T) checkoutRuleFixture {
	t.Helper()
	f := newSeededShadowFixture(t)
	return checkoutRuleFixture{
		shadowFixture: f,
		leaf:          filepath.Join(f.repo, ".claude", "worktrees", "w1"),
		reg:           filepath.Join(f.gitDir, worktreesSubdir, "w1"),
	}
}

func (f checkoutRuleFixture) create(t *testing.T, extra map[string]any) (string, []envCall) {
	t.Helper()
	params := map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": f.leaf}
	for k, v := range extra {
		params[k] = v
	}
	return f.call(t, "git.worktree_create", params)
}

// addConfig appends text to .git/config of the repository.
func (f checkoutRuleFixture) addConfig(t *testing.T, text string) {
	t.Helper()
	config := filepath.Join(f.gitDir, "config")
	mustWrite(t, config, mustRead(t, config)+text)
}

// filterSections is n sections [filter "f<i>"], each with the lines body.
func filterSections(n int, body string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "[filter \"f%d\"]\n%s", i, body)
	}
	return b.String()
}

// wantGone checks that the leaf, the registration and the branch w1 are gone.
func (f checkoutRuleFixture) wantGone(t *testing.T) {
	t.Helper()
	for _, p := range []string{f.leaf, f.reg} {
		if exists(p) {
			t.Errorf("%s stays, want it gone", p)
		}
	}
	if branchExists(t, f.repo, "w1") {
		t.Error("the branch w1 stays, want it gone")
	}
}

// readTreeArgs is the argv of the read-tree call, or nil.
func readTreeArgs(calls []envCall) []string {
	for _, c := range calls {
		if slices.Contains(c.argv, "read-tree") {
			return c.argv
		}
	}
	return nil
}

// TestCheckoutFilterNameCap: more than 64 filter names in the repository's own
// config refuse the create after the add. No read-tree call runs, and the
// rollback removes everything. Cells C2 to C4 of round task008-01 show it on
// Linux and macOS VMs, and C2 on a Windows VM too. 64 names pass, also with
// more keys (cells C1 and C5a). "shadowDirOff":true turns the cap off (cell
// C5b).
func TestCheckoutFilterNameCap(t *testing.T) {
	const smudge = "\tsmudge = false\n"
	refused := failedFrame(t, "git worktree add failed (checkout): this repository's git config cannot be used: it defines more than 64 filters")
	for _, row := range []struct {
		cell, config, user string
		extra              map[string]any
		refused            bool
	}{
		{cell: "C1 64 names", config: filterSections(64, smudge)},
		{cell: "C2 65 names", config: filterSections(65, smudge), refused: true},
		{cell: "C3 65 names with only clean", config: filterSections(65, "\tclean = x\n"), refused: true},
		{cell: "C4 65 names the user config defines too", config: filterSections(65, smudge),
			user: filterSections(65, "\tsmudge = cat\n"), refused: true},
		{cell: "C5a 64 names with more keys", config: filterSections(64, smudge+"\tclean = x\n\trequired = true\n")},
		{cell: "C5b 65 names with shadowDirOff", config: filterSections(65, smudge), extra: map[string]any{"shadowDirOff": true}},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newCheckoutRuleFixture(t)
			f.addConfig(t, row.config)
			if row.user != "" {
				outside(t, row.user)
			}
			raw, calls := f.create(t, row.extra)
			if !row.refused {
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("git.worktree_create = %s, want success", raw)
				}
				return
			}
			if raw != refused {
				t.Fatalf("git.worktree_create = %s\nwant %s", raw, refused)
			}
			if ranReadTree(calls) {
				t.Error("a read-tree call ran, want none")
			}
			f.wantGone(t)
		})
	}
}

// ownConfigText is the refusal of a second listing of the checkout that failed
// with cause.
func ownConfigText(cause string) string {
	return "git worktree add failed (checkout): Claude could not read your own git config on the remote machine (" + cause +
		"). Run git config --list there to see the problem."
}

// stubSecondListing makes the stub answer the second listing of the checkout,
// the one with GIT_INDEX_FILE, with exit 1 after d.
func stubSecondListing(t *testing.T, d time.Duration) {
	t.Helper()
	slowGit(t, "config,-z,--list", "fail", d, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_INDEX_FILE=index")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "1")
}

// TestCheckoutOwnConfigListingFails: a second listing of the checkout that
// exits 1 refuses the create with its exit status. No read-tree call runs, and
// the rollback removes everything. Cell C10 of round task008-01 shows it on
// Linux, macOS and Windows VMs.
func TestCheckoutOwnConfigListingFails(t *testing.T) {
	f := newCheckoutRuleFixture(t)
	stubSecondListing(t, 0)
	raw, calls := f.create(t, nil)
	if want := failedFrame(t, ownConfigText("exit status 1")); raw != want {
		t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if ranReadTree(calls) {
		t.Error("a read-tree call ran, want none")
	}
	f.wantGone(t)
}

// TestCheckoutOwnConfigListingStops: the bound ends a second listing that does
// not end, and the create is refused with the stop text. Cell C11 of round
// task008-01 shows it on Linux and macOS VMs. The seam shrinks the bound of 30
// s to 1 s. The stub sleeps 60 s, so a reply before that shows the end.
func TestCheckoutOwnConfigListingStops(t *testing.T) {
	f := newCheckoutRuleFixture(t)
	old := ownConfigBound
	t.Cleanup(func() { ownConfigBound = old })
	ownConfigBound = time.Second
	stubSecondListing(t, 60*time.Second)
	start := time.Now()
	raw, calls := f.create(t, nil)
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("the reply came after %v, want it soon after the bound", took)
	}
	if want := failedFrame(t, ownConfigText("git took too long: Claude stopped git config after 30 seconds; try again")); raw != want {
		t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if ranReadTree(calls) {
		t.Error("a read-tree call ran, want none")
	}
	f.wantGone(t)
}

// TestCheckoutOwnConfigListingOutlivesDeadline: the `timeoutMs` of the caller
// does not end the second listing. The stub holds it 3.5 s past a deadline of
// 2000 ms and then runs git. After it, 65 names get the cap text (cell P1)
// and 64 names get the timeout frame (cell P2). Round task008-05 shows both
// frames on a Linux VM.
func TestCheckoutOwnConfigListingOutlivesDeadline(t *testing.T) {
	const hold = 3500 * time.Millisecond
	for _, row := range []struct {
		cell  string
		names int
		want  string
	}{
		{"P1 65 names", 65, `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed (checkout): this repository's git config cannot be used: it defines more than 64 filters","errorCode":"worktree_add_failed"}}`},
		{"P2 64 names", 64, `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add timed out after 2000ms (deadline expired during the checkout): context deadline exceeded","errorCode":"timeout"}}`},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newCheckoutRuleFixture(t)
			f.addConfig(t, filterSections(row.names, "\tsmudge = false\n"))
			slowGit(t, "config,-z,--list", "pre", hold, "", "")
			t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_INDEX_FILE=index")
			start := time.Now()
			raw, calls := f.create(t, map[string]any{"timeoutMs": 2000})
			if took := time.Since(start); took < hold {
				t.Errorf("the reply came after %v, want it after the held listing (%v)", took, hold)
			}
			if raw != row.want {
				t.Fatalf("git.worktree_create = %s\nwant %s", raw, row.want)
			}
			if ranReadTree(calls) {
				t.Error("a read-tree call ran, want none")
			}
			f.wantGone(t)
		})
	}
}

// TestOwnConfigBoundIs30Seconds pins the production bound of the second
// listing (round task008-01, cells C11 and C11b, Linux and macOS VMs).
func TestOwnConfigBoundIs30Seconds(t *testing.T) {
	if ownConfigBound != 30*time.Second {
		t.Errorf("ownConfigBound = %v, want 30s", ownConfigBound)
	}
}

// TestCheckoutUserLfsStorage: with lfs.storage in the user config the
// read-tree call has no lfs.storage item. Without it the call has the item
// (round task008-01, cell C12, Linux, macOS and Windows VMs).
func TestCheckoutUserLfsStorage(t *testing.T) {
	for _, user := range []bool{false, true} {
		t.Run(fmt.Sprint("user lfs.storage ", user), func(t *testing.T) {
			f := newCheckoutRuleFixture(t)
			if user {
				outside(t, "[lfs]\n\tstorage = "+filepath.ToSlash(filepath.Join(t.TempDir(), "lfsx"))+"\n")
			}
			raw, calls := f.create(t, nil)
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s, want success", raw)
			}
			args := readTreeArgs(calls)
			has := slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "lfs.storage=") })
			if has == user {
				t.Errorf("read-tree call %q: lfs.storage item %v, want %v", args, has, !user)
			}
		})
	}
}

// TestCheckoutRepositoryRequiresUserSmudge: the user config gives the filter f
// a smudge command that fails, and .git/config sets only `required = true`.
// The read-tree call gets only the pair filter.f.required=true. The command
// of the user config runs, and its failure fails the create with git's text.
// Cell C6 of round task008-01 shows it on Linux and macOS VMs. `required =
// yes` and `required = 1` do the same (round task008-03, cells R1 and R2,
// Linux VM). The command is this test binary in a helper mode that exits 5.
func TestCheckoutRepositoryRequiresUserSmudge(t *testing.T) {
	for _, value := range []string{"true", "yes", "1"} {
		t.Run(value, func(t *testing.T) {
			f := newCheckoutRuleFixture(t)
			mustWrite(t, filepath.Join(f.repo, ".gitattributes"), "a.txt filter=f\n")
			mustWrite(t, filepath.Join(f.repo, "a.txt"), "a\n")
			runGit(t, f.repo, "add", ".gitattributes", "a.txt")
			runGit(t, f.repo, "commit", "-q", "-m", "filter")
			f.addConfig(t, "[filter \"f\"]\n\trequired = "+value+"\n")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			outside(t, "[filter \"f\"]\n\tsmudge = CLAUSTRUM_TEST_HELPER=stderr-exit5 '"+filepath.ToSlash(exe)+"'\n")
			raw, calls := f.create(t, nil)
			if !strings.Contains(raw, `"error":"git worktree add failed (checkout): `) || !strings.Contains(raw, "smudge filter f failed") ||
				!strings.HasSuffix(raw, `"errorCode":"worktree_add_failed"}}`) {
				t.Fatalf("git.worktree_create = %s\nwant the failed checkout with git's text", raw)
			}
			args := readTreeArgs(calls)
			want := []string{"-c", "filter.f.required=true", "-c", "lfs.storage=" + filepath.Join(f.gitDir, "lfs")}
			if len(args) < len(want) || !slices.Equal(args[:len(want)], want) {
				t.Errorf("the read-tree call starts %q\nwant %q", args[:min(len(args), len(want))], want)
			}
			f.wantGone(t)
		})
	}
}

// TestCheckoutRequiredUserSmudgeFallbackGit: the state of cell C6 in the
// state fallback-git. The read-tree call gets no filter pair, git reads
// `required = true` from the repository config, and the failure of the user
// smudge fails the create (round task008-03, cell R7, Linux
// VM).
func TestCheckoutRequiredUserSmudgeFallbackGit(t *testing.T) {
	f := newCheckoutRuleFixture(t)
	h := f.s.gitShadow
	h.mu.Lock()
	h.state, h.tested = controlFallback, shadowClock()
	h.mu.Unlock()
	mustWrite(t, filepath.Join(f.repo, ".gitattributes"), "a.txt filter=f\n")
	mustWrite(t, filepath.Join(f.repo, "a.txt"), "a\n")
	runGit(t, f.repo, "add", ".gitattributes", "a.txt")
	runGit(t, f.repo, "commit", "-q", "-m", "filter")
	f.addConfig(t, "[filter \"f\"]\n\trequired = true\n")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	outside(t, "[filter \"f\"]\n\tsmudge = CLAUSTRUM_TEST_HELPER=stderr-exit5 '"+filepath.ToSlash(exe)+"'\n")
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, "smudge filter f failed") || !strings.HasSuffix(raw, `"errorCode":"worktree_add_failed"}}`) {
		t.Fatalf("git.worktree_create = %s\nwant the failed checkout with git's text", raw)
	}
	args := readTreeArgs(calls)
	if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "filter.") }) {
		t.Errorf("the read-tree call %q has a filter pair, want none", args)
	}
	f.wantGone(t)
}

// TestCheckoutRefusalOrder: in the state shadow the second listing runs first,
// then the cap, then a refusal of one name. With 65 names the cap text wins
// over a name with "=" (cell R3) and over a name of 201 bytes (cell R4). A
// failed second listing wins over the cap (cell R5). Round task008-03 shows
// that on a Linux VM. One name with "=" still gets its own text, after the
// second listing.
func TestCheckoutRefusalOrder(t *testing.T) {
	const smudge = "\tsmudge = false\n"
	const capText = "this repository's git config cannot be used: it defines more than 64 filters"
	eqText := "this repository's git config cannot be used: the filter \"a=b\" is defined only there, and Claude cannot handle \"=\" in a filter's name; define it in ~/.gitconfig on the remote machine, or rename it"
	for _, row := range []struct {
		cell, config string
		failListing  bool
		want         string
	}{
		{"R3 65 names and a=b", filterSections(65, smudge) + "[filter \"a=b\"]\n" + smudge, false, capText},
		{"R4 65 names and a long name", filterSections(65, smudge) + "[filter \"" + strings.Repeat("n", 201) + "\"]\n" + smudge, false, capText},
		{"R5 65 names and a failed listing", filterSections(65, smudge), true, "Claude could not read your own git config on the remote machine (exit status 1). Run git config --list there to see the problem."},
		{"a=b alone", "[filter \"a=b\"]\n" + smudge, false, eqText},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newCheckoutRuleFixture(t)
			f.addConfig(t, row.config)
			if row.failListing {
				stubSecondListing(t, 0)
			}
			raw, calls := f.create(t, nil)
			if want := failedFrame(t, "git worktree add failed (checkout): "+row.want); raw != want {
				t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
			}
			second := slices.ContainsFunc(calls, func(c envCall) bool {
				return slices.Contains(c.argv, "--list") && slices.ContainsFunc(c.env, func(kv string) bool { return strings.HasPrefix(kv, "GIT_INDEX_FILE=") })
			})
			if !second {
				t.Error("no second listing with GIT_INDEX_FILE ran before the refusal")
			}
			if ranReadTree(calls) {
				t.Error("a read-tree call ran, want none")
			}
			f.wantGone(t)
		})
	}
}

// TestPlanForSharedFilter: the plan of a filter that both configs define. Only
// the state of cell C6 (round task008-01) gets the one `required` pair, also
// with `yes` and `1` (round task008-03, cells R1 and R2). The states next to
// it keep the pairs with the outside values. In the state fallback-git the C6
// state gets no pair (cell R7 of round task008-03). A shared name
// with "=" is still refused.
func TestPlanForSharedFilter(t *testing.T) {
	recs := func(r ...string) []byte { return []byte(strings.Join(r, "\x00") + "\x00") }
	for _, row := range []struct {
		name          string
		outside, repo []string
		want          []string
		fallback      bool
		refusal       string
	}{
		{name: "C6", outside: []string{"filter.f.smudge\nrun-me"}, repo: []string{"filter.f.required\ntrue"},
			want: []string{"-c", "filter.f.required=true"}},
		{name: "R1 yes", outside: []string{"filter.f.smudge\nrun-me"}, repo: []string{"filter.f.required\nyes"},
			want: []string{"-c", "filter.f.required=true"}},
		{name: "R2 1", outside: []string{"filter.f.smudge\nrun-me"}, repo: []string{"filter.f.required\n1"},
			want: []string{"-c", "filter.f.required=true"}},
		{name: "ON", outside: []string{"filter.f.smudge\nrun-me"}, repo: []string{"filter.f.required\nON"},
			want: []string{"-c", "filter.f.required=true"}},
		{name: "bare key", outside: []string{"filter.f.smudge\nrun-me"}, repo: []string{"filter.f.required"},
			want: []string{"-c", "filter.f.required=true"}},
		{name: "R7 fallback-git", outside: []string{"filter.f.smudge\nrun-me"}, repo: []string{"filter.f.required\ntrue"},
			fallback: true},
		{name: "a=b", outside: []string{"filter.a=b.smudge\nrun-me"}, repo: []string{"filter.a=b.required\ntrue"},
			refusal: unreplaceableFilterText("a=b")},
		{name: "user smudge cat", outside: []string{"filter.f.smudge\ncat"}, repo: []string{"filter.f.required\ntrue"},
			want: []string{"-c", "filter.f.required=false", "-c", "filter.f.smudge=cat"}},
		{name: "another required value", outside: []string{"filter.f.smudge\nrun-me"}, repo: []string{"filter.f.required\nfalse"},
			want: []string{"-c", "filter.f.required=false", "-c", "filter.f.smudge=run-me"}},
		{name: "user required too", outside: []string{"filter.f.smudge\nrun-me", "filter.f.required\nfalse"}, repo: []string{"filter.f.required\ntrue"},
			want: []string{"-c", "filter.f.required=false", "-c", "filter.f.smudge=run-me"}},
		{name: "repository smudge too", outside: []string{"filter.f.smudge\nrun-me"}, repo: []string{"filter.f.required\ntrue", "filter.f.smudge\nown"},
			want: []string{"-c", "filter.f.required=false", "-c", "filter.f.smudge=run-me"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			plan := planFromListings(recs(append(slices.Clone(row.outside), row.repo...)...), recs(row.outside...), !row.fallback)
			if !slices.Equal(plan.args, row.want) || plan.refusal != row.refusal || len(plan.refused) != 0 {
				t.Errorf("plan %q, refusal %q, refused %q\nwant %q", plan.args, plan.refusal, plan.refused, row.want)
			}
		})
	}
}

// TestRepositoryFilterCount: the count takes the records of the repository
// only, and one name once. A record that the user config has too is taken
// from outside once, so the copy in the repository still counts.
func TestRepositoryFilterCount(t *testing.T) {
	recs := func(r ...string) []byte { return []byte(strings.Join(r, "\x00") + "\x00") }
	outside := recs("filter.u.smudge\ncat", "filter.s.smudge\nx")
	full := recs("filter.u.smudge\ncat", "filter.s.smudge\nx",
		"filter.a.smudge\nx", "filter.a.clean\ny", "filter.b.clean\nx", "filter.s.smudge\nx", "filter.short\nx", "core.bare\nfalse")
	if got := repositoryFilterCount(full, outside); got != 3 {
		t.Errorf("repositoryFilterCount = %d, want 3 (a, b and s)", got)
	}
	var many []string
	for i := range filterNameMaxCount + 1 {
		many = append(many, fmt.Sprintf("filter.f%d.clean\nx", i))
	}
	if plan := planFromListings(recs(many...), nil, true); !plan.tooManyFilters {
		t.Error("65 names: tooManyFilters is false, want true")
	}
	if plan := planFromListings(recs(many[1:]...), nil, true); plan.tooManyFilters {
		t.Error("64 names: tooManyFilters is true, want false")
	}
}
