package main

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The filter rule of the git.worktree_create checkout. 5fd08069 refuses a create
// whose checkout needs a git filter that only the repository's own git config
// defines. 89cb6289 runs that filter. The cell names in the comments are those
// of the VM rounds. A Linux VM with git 2.43.0 ran the
// cells, and a Windows VM ran a part of them.

// filterKeys are the keys of a filter section that the checkout reads, in sorted
// order. A `clean` key is not among them: it makes no definition (cells A3, A7
// and A8).
var filterKeys = [...]string{"process", "required", "smudge"}

const (
	filterProcess = iota
	filterRequired
	filterSmudge
)

const (
	// filterNameMaxBytes is the longest name of a filter that gets the three pairs.
	// A name of 200 bytes gets them, and one of 201 bytes is refused (cells Q4,
	// Linux and Windows VMs).
	filterNameMaxBytes = 200
	// filterNameStartBytes is the part of a long name that the refusal quotes.
	filterNameStartBytes = 40
	// passSmudge is the one smudge value of the repository that passes under every
	// name (cell V1). `cat ` with a space after it, /bin/cat, `cat -`, `cat %f` and
	// `true` are refused (cells V2 to V6).
	passSmudge = "cat"
	// lfsFilterName is the one filter name with two more values that pass (cells V7
	// and V13). The same smudge value under the name m is refused (cell V8), and so
	// is `git lfs smudge -- %f` under the name lfs (cell V9).
	lfsFilterName = "lfs"
	lfsSmudge     = "git-lfs smudge -- %f"
	lfsProcess    = "git-lfs filter-process"
	// filterNameMaxCount is the most filter names that the repository's own
	// config can have. 64 names pass and 65 are refused with tooManyFiltersText.
	// Cells C1 and C2 of round task008-01 show it on Linux, macOS and Windows VMs.
	filterNameMaxCount = 64
	// tooManyFiltersText is the refusal of more than filterNameMaxCount names.
	tooManyFiltersText = "this repository's git config cannot be used: it defines more than 64 filters"
)

// jsonTrueSwitch is a request member that is on for the JSON value true and off for
// every other value, with no error. On 5fd08069 a git.worktree_create with the
// string "true", the number 1, null, false or {} refuses the filter as with no
// member (cells Q12, Linux VM, and four of them on a Windows VM).
type jsonTrueSwitch bool

func (b *jsonTrueSwitch) UnmarshalJSON(data []byte) error {
	*b = string(bytes.TrimSpace(data)) == "true"
	return nil
}

// filterValues holds what one listing gives a filter name, by key (filterKeys).
// has reports that the listing holds the key. last is its last value: git uses
// the last value of a key (cells Q8).
type filterValues struct {
	has  [len(filterKeys)]bool
	last [len(filterKeys)]string
}

// defined reports whether the listing defines the filter: a `smudge` or a
// `process` value that is not empty. A `clean` or a `required` key makes no
// definition (cells A3, A4, A8 and O4), and an empty `smudge` makes none (cells
// A5 and O5).
func (v filterValues) defined() bool {
	return v.last[filterSmudge] != "" || v.last[filterProcess] != ""
}

// parseFilterListing reads the filter keys of a `git config -z --list` output
// (parseConfigListing gives the record form). The name of a filter is the text
// between the first and the last dot of the key, as git lists it: git keeps the
// letter case of the name and lowers the key (git 2.43.0 and 2.47.3). So a key
// spelled `Smudge` counts as `smudge` here. Cell V14 shows only that the value
// `cat` under `Smudge` is not refused. The names M and m are two filters (cell
// D3).
//
// A `required` key with no value counts as true, as git reads it (not measured).
// A `smudge` or a `process` key with no value counts as empty. So a repository
// with such a `smudge` key gets no pair, and git fails the checkout with its own
// text. 5fd08069 answers the filter refusal there (cell Q10). That difference
// stays.
func parseFilterListing(out []byte) map[string]filterValues {
	defs := map[string]filterValues{}
	for rec := range bytes.SplitSeq(out, []byte{0}) {
		key, value, hasValue := strings.Cut(string(rec), "\n")
		rest, ok := strings.CutPrefix(key, "filter.")
		last := strings.LastIndexByte(rest, '.')
		if !ok || last < 0 {
			continue
		}
		k := slices.Index(filterKeys[:], rest[last+1:])
		if k < 0 {
			continue
		}
		if !hasValue && k == filterRequired {
			value = "true"
		}
		v := defs[rest[:last]]
		v.has[k], v.last[k] = true, value
		defs[rest[:last]] = v
	}
	return defs
}

// repositoryValuesPass reports whether the `smudge` and `process` values of a
// filter that only the repository defines are ones that pass. An empty value
// passes. With mixed values the filter is refused: `smudge = cat` beside a
// `process` value (cell V11), and an empty `process` beside a `smudge` value that
// does not pass (cell Q9). With lfsProgram set, the two git-lfs values do not
// pass (repositoryLfsProgramKey).
func repositoryValuesPass(name string, v filterValues, lfsProgram bool) bool {
	lfs := name == lfsFilterName && !lfsProgram
	if s := v.last[filterSmudge]; s != "" && s != passSmudge && (!lfs || s != lfsSmudge) {
		return false
	}
	if p := v.last[filterProcess]; p != "" && (!lfs || p != lfsProcess) {
		return false
	}
	return true
}

// filterPlan is what the filter rule gives the read-tree checkout.
type filterPlan struct {
	// args are the -c pairs of the checkout. They come before the pairs of the
	// profile.
	args []string
	// refused are the names of the filters that got the three pairs.
	refused []string
	// refusal is the text of a refusal that needs no checkout, or "".
	refusal string
	// lfsKey is the git-lfs program key of the repository, when the filter lfs
	// of the outside config got the three pairs for it. It is "" otherwise.
	lfsKey string
	// listing is the config precursor of the checkout, when the rule ran it.
	listing *listingRun
	// tooManyFilters is true when the repository's own config has more than
	// filterNameMaxCount filter names (repositoryFilterCount).
	tooManyFilters bool
	// userLfsStorage is true when the config outside the repository sets
	// lfs.storage. The read-tree call then gets no lfs.storage item (round
	// task008-01, cell C12, Linux, macOS and Windows VMs, with the user config).
	// The system config and the config of the daemon count too: that is
	// claustrum's own choice.
	userLfsStorage bool
}

// pairs appends one -c pair for each key of keys, with the values by key.
func (f *filterPlan) pairs(name string, values [len(filterKeys)]string, keys ...int) {
	for _, k := range keys {
		f.args = append(f.args, "-c", "filter."+name+"."+filterKeys[k]+"="+values[k])
	}
}

// planFilters turns the filters of two listings into the -c pairs of the
// checkout. full is the listing that the checkout reads. outside is the listing
// with no repository in view. The names go in sorted order (cells N1 to N4).
//
// A filter that full defines and outside does not is one that only the
// repository defines:
//
//   - With a value that does not pass, it gets three pairs:
//     `filter.<name>.process=`, `filter.<name>.required=true` and
//     `filter.<name>.smudge=false`. git then fails at the first path that uses the
//     filter. A filter that no path uses gets the pairs too (cell N4, and cells
//     C1, C7 and D3 on a Windows VM). On the development host, git 2.47.3 starts
//     no filter program with a `process` value that is set and empty, and
//     `required` makes that a failure. A name over filterNameMaxBytes is refused
//     with no checkout (cells D5 and Q4).
//   - `smudge = cat` gets three pairs that turn the filter off: an empty `process`,
//     `required=false` and an empty `smudge`. On 5fd08069 a `cat` first on PATH
//     does not run, and its read-tree call carries no pair (cell Q7, Linux VM).
//     On the development host these
//     pairs start no program and give the same file, also with `required = true`
//     in the repository. A long name passes here (cell Q5).
//   - The filter lfs with values that pass gets its own value again as a pair
//     (cells V7 and V13). With lfsKey set those values do not pass
//     (repositoryLfsProgramKey).
//   - A name with "=" is refused with no checkout (cell Q3). git ends the key of
//     a -c pair at the first "=". Cell Q3 has a value that does not pass. Such a
//     name with a value that passes is not measured.
//
// One filter that outside defines does not run: the filter lfs, when lfsKey is
// set. It gets the three pairs too. A failure at it answers lfsProgramText, not
// the refusal of a filter that only the repository defines. With no path under
// the filter the checkout succeeds. A Windows VM ran this rule for the
// lfs.customtransfer keys and the standalone keys (cells L1 to L8).
//
// Every other filter that outside defines runs with the outside values, as on
// 5fd08069 in cells A7, O6 and Q6 (the `smudge` key). If full differs from outside in one of the three keys,
// the filter gets pairs with the outside values. Those pairs are claustrum's own:
// 5fd08069 passes none there. One state gets only the `required` pair of the
// repository (repositoryRequiresUserSmudge). Two states get claustrum's own refusal there,
// because no -c pair gives the result (unreplaceableFilterText). The first is a
// `process` key in full and none in outside: a pair cannot remove a key, and an
// empty `process` value stops the outside `smudge` filter too. 5fd08069 runs the
// outside filter and succeeds there (cell Q1). That difference stays. The second
// is a name with "=" or a name that is not valid UTF-8 (not measured).
//
// A name that is not valid UTF-8 cannot go through a Windows command line as it
// is, so a pair with it names another filter there. A filter that only the
// repository defines is refused with such a name, on every system. The text is
// claustrum's own. 5fd08069 passes its three pairs: on a Linux VM its checkout
// fails, and on a Windows VM it answers success and no filter runs (cells S3
// and X1). That difference stays.
//
// shadow is true in the state shadow. It decides the pair of
// repositoryRequiresUserSmudge.
func planFilters(full, outside map[string]filterValues, lfsKey string, shadow bool) filterPlan {
	var f filterPlan
	for _, name := range slices.Sorted(maps.Keys(full)) {
		v, o := full[name], outside[name]
		switch {
		case o.defined() && name == lfsFilterName && lfsKey != "":
			// The repository names a program for git-lfs. The filter lfs of
			// the outside config does not run for this checkout.
			f.pairs(name, [...]string{"", "true", "false"}, filterProcess, filterRequired, filterSmudge)
			f.refused = append(f.refused, name)
			f.lfsKey = lfsKey
		case o.defined():
			if v == o {
				continue
			}
			if (v.has[filterProcess] && !o.has[filterProcess]) || strings.Contains(name, "=") || !utf8.ValidString(name) {
				f.refusal = unreplaceableFilterText(name)
				return f
			}
			if repositoryRequiresUserSmudge(v, o) {
				if shadow {
					f.pairs(name, [...]string{"", "true", ""}, filterRequired)
				}
				continue
			}
			if !o.has[filterRequired] {
				o.last[filterRequired] = "false"
			}
			if o.has[filterProcess] {
				f.pairs(name, o.last, filterProcess)
			}
			f.pairs(name, o.last, filterRequired, filterSmudge)
		case !v.defined():
		case strings.Contains(name, "="):
			f.refusal = "this repository's git config cannot be used: the filter " + strconv.Quote(name) +
				" is defined only there, and Claude cannot handle \"=\" in a filter's name; define it in ~/.gitconfig on the remote machine, or rename it"
			return f
		case !utf8.ValidString(name):
			f.refusal = "this repository's git config cannot be used: the filter " + strconv.Quote(name) +
				" is defined only there, and its name is not valid UTF-8; define it in ~/.gitconfig on the remote machine, or rename it"
			return f
		case !repositoryValuesPass(name, v, lfsKey != ""):
			if len(name) > filterNameMaxBytes {
				f.refusal = "this repository's git config cannot be used: a filter defined only there has a name longer than " +
					strconv.Itoa(filterNameMaxBytes) + " bytes (it starts " + strconv.Quote(name[:filterNameStartBytes]) +
					"); define it in ~/.gitconfig on the remote machine, or rename it"
				return f
			}
			f.pairs(name, [...]string{"", "true", "false"}, filterProcess, filterRequired, filterSmudge)
			f.refused = append(f.refused, name)
		case v.last[filterProcess] != "":
			f.pairs(name, v.last, filterProcess)
			if v.last[filterSmudge] == lfsSmudge {
				f.pairs(name, v.last, filterSmudge)
			}
		case v.last[filterSmudge] == lfsSmudge:
			f.pairs(name, v.last, filterSmudge)
		default:
			f.pairs(name, [...]string{"", "false", ""}, filterProcess, filterRequired, filterSmudge)
		}
	}
	return f
}

// repositoryRequiresUserSmudge reports one state of a filter that both configs
// define. Outside the repository it has a `smudge` command other than `cat`
// and no other key that the checkout reads. The repository adds only
// `required = true`. In the state shadow the filter then gets the one pair
// `filter.<name>.required=true`, and the smudge command of the user config
// runs. So a failure of that command fails the checkout with git's text (round
// task008-01, cell C6, Linux and macOS VMs). `required = yes` and `required =
// 1` get the same pair (round task008-03, cells R1 and R2, Linux VM). `on`,
// other letter case and a bare key are claustrum's own. In the state
// fallback-git the filter gets no pair, and the checkout fails the same way
// (round task008-03, cell R7, Linux VM). Every other state of
// a shared name keeps the pairs of planFilters (not measured).
func repositoryRequiresUserSmudge(v, o filterValues) bool {
	onlySmudge := [len(filterKeys)]bool{filterSmudge: true}
	return o.has == onlySmudge && o.last[filterSmudge] != passSmudge &&
		v.has == [len(filterKeys)]bool{filterRequired: true, filterSmudge: true} &&
		v.last[filterSmudge] == o.last[filterSmudge] && gitTrue(v.last[filterRequired])
}

// gitTrue reports whether value is `true`, `yes`, `on` or `1`, in any letter
// case.
func gitTrue(value string) bool {
	return slices.Contains([]string{"true", "yes", "on", "1"}, strings.ToLower(value))
}

// repositoryLfsProgramKey is the first key, in sorted order, of the repository's
// own config that names a program for git-lfs, or "". Such a key starts with
// `lfs.customtransfer.` or with `lfs.extension.`, or it starts with `lfs.` and
// ends with `.standalonetransferagent`. The last form covers
// lfs.standalonetransferagent and lfs.<url>.standalonetransferagent. The
// `lfs.extension.` keys are from the help text of git-lfs 3.7.1, which gives
// them a command. No VM ran a cell with such a key. full and outside are the two plain
// listings. A record that outside has too is not the repository's own, so the
// same keys in the user or the system config do not count.
//
// A working git-lfs 3.7.1 runs the program of lfs.customtransfer.<x>.path on
// 89cb6289 and on build C of this change, and it does not run on
// 5fd08069 (cell X4, Windows VM). So with one of these keys the filter lfs does
// not run for the checkout, wherever it is defined (planFilters). That
// rule is claustrum's own: on 5fd08069 git-lfs runs and fails by itself. With
// build D the agent ran in the same cells as on 5fd08069: with the keys
// in the user config only, and with shadowDirOff (cells L1 to L8, Windows VM).
func repositoryLfsProgramKey(full, outside []byte) string {
	other := map[string]bool{}
	for rec := range bytes.SplitSeq(outside, []byte{0}) {
		other[string(rec)] = true
	}
	first := ""
	for rec := range bytes.SplitSeq(full, []byte{0}) {
		key, _, _ := strings.Cut(string(rec), "\n")
		named := strings.HasPrefix(key, "lfs.customtransfer.") || strings.HasPrefix(key, "lfs.extension.") ||
			(strings.HasPrefix(key, "lfs.") && strings.HasSuffix(key, ".standalonetransferagent"))
		if named && !other[string(rec)] && (first == "" || key < first) {
			first = key
		}
	}
	return first
}

// planFromListings is planFilters for two plain listings: the one that
// the checkout reads, and the part of it from outside the repository.
func planFromListings(full, outside []byte, shadow bool) filterPlan {
	f := planFilters(parseFilterListing(full), parseFilterListing(outside), repositoryLfsProgramKey(full, outside), shadow)
	f.tooManyFilters = repositoryFilterCount(full, outside) > filterNameMaxCount
	f.userLfsStorage = slices.ContainsFunc(bytes.Split(outside, []byte{0}), func(rec []byte) bool {
		key, _, _ := bytes.Cut(rec, []byte{'\n'})
		return string(key) == "lfs.storage"
	})
	return f
}

// repositoryFilterCount is the number of filter names in the repository's own
// config. Those are the records of full that outside does not hold, with each
// record of outside taken once. A name counts once, whatever keys it has. A
// name with a `smudge` key counts (cell C2 of round task008-01, Linux, macOS
// and Windows VMs). So does one with only a `clean` key (cell C3, Linux and
// macOS VMs). A name counts also when the user config defines it with `smudge
// = cat` (cell C4). Other user values are claustrum's own. More keys on one
// name do not add to the count (cell C5a). A name with only other keys counts
// too, and the config of the daemon and of the system counts as outside. Both
// are claustrum's own choices.
func repositoryFilterCount(full, outside []byte) int {
	other := map[string]int{}
	for rec := range bytes.SplitSeq(outside, []byte{0}) {
		other[string(rec)]++
	}
	names := map[string]bool{}
	for rec := range bytes.SplitSeq(full, []byte{0}) {
		if other[string(rec)] > 0 {
			other[string(rec)]--
			continue
		}
		key, _, _ := strings.Cut(string(rec), "\n")
		rest, ok := strings.CutPrefix(key, "filter.")
		if last := strings.LastIndexByte(rest, '.'); ok && last >= 0 {
			names[rest[:last]] = true
		}
	}
	return len(names)
}

// lfsProgramText is the refusal text when the checkout fails at the filter lfs
// that the outside config defines, and key is the program key of the
// repository. The text is claustrum's own.
func lfsProgramText(key string) string {
	return "this repository's git config cannot be used: it names a program for git-lfs (" + strconv.Quote(key) +
		"); remove that setting, or put it into ~/.gitconfig on the remote machine"
}

// unreplaceableFilterText is the refusal text for a filter whose values of the
// repository no -c pair can replace (planFilters). The text is
// claustrum's own.
func unreplaceableFilterText(name string) string {
	return "the git filter " + strconv.Quote(name) + " has values in this repository's git config that no option of git can replace for the checkout"
}

// outsideListingEnv is the environment of the listing with no repository in
// view, for a git that has no --show-scope. GIT_DIR names the null device, as in
// the excludes read (resolveUserExcludesFile), so git reads no config of a
// repository, whatever its working directory. The daemon's own GIT_DIR,
// GIT_COMMON_DIR and GIT_WORK_TREE go. Every other entry is that of the light
// config precursor (precursorEnv): HOME, XDG_CONFIG_HOME, GIT_CONFIG_GLOBAL,
// GIT_CONFIG_SYSTEM and the GIT_CONFIG_COUNT set of the daemon stay as they are.
func outsideListingEnv() []string {
	env := slices.DeleteFunc(precursorEnv(false, nil), func(kv string) bool {
		n := envName(kv)
		return n == "GIT_DIR" || n == "GIT_COMMON_DIR" || n == "GIT_WORK_TREE"
	})
	return append(env, "GIT_DIR="+os.DevNull)
}

// gitUsageExit is the exit code of a git that refuses an option of its command
// line. A git before 2.26 answers --show-scope so. Cell Q14 has a stub with
// that code.
const gitUsageExit = 129

// splitScopedListing reads the output of `git config -z --list --show-scope`.
// Each entry is the scope, a NUL byte, and one record of the plain listing. It
// returns two plain listings: every record, and the records from outside the
// repository. The scopes `local` and `worktree` are the repository's own config:
// .git/config, a file that it includes, and config.worktree (cells A1, B1 and
// B2). Every other scope is outside it: the system config, the user config with
// its includes, and the GIT_CONFIG_COUNT set of the daemon (cells B3 to B6). A
// file that an `includeIf "gitdir:…"` section of the user config names is
// outside too, and 5fd08069 runs a filter of such a file (cell R1).
func splitScopedListing(out []byte) (full, outside []byte) {
	recs := bytes.Split(out, []byte{0})
	for i := 0; i+1 < len(recs); i += 2 {
		full = append(append(full, recs[i+1]...), 0)
		if scope := string(recs[i]); scope != "local" && scope != "worktree" {
			outside = append(append(outside, recs[i+1]...), 0)
		}
	}
	return full, outside
}

// listFilterPlan runs the listings of the filter rule and returns the plan
// of planFilters. Each listing runs in leaf, the new worktree.
//
// The first call is `--git-dir=<gitDir> config -z --list --show-scope`, with the
// light precursorEnv. It gives both sides in one listing (splitScopedListing).
// The config precursor of the checkout follows it as before.
//
// A git that refuses the option (gitUsageExit) gets two plain listings in its
// place. One has outsideListingEnv and lists the config outside the repository.
// The other is the config precursor of the checkout, which then runs no listing
// of its own. 5fd08069 makes no --show-scope call, so the stub of cell Q14
// changes nothing for it. On
// this path an `includeIf "gitdir:…"` section of the user config that matches
// the repository is in the second listing only. So a filter of such a section
// counts as one that only the repository defines, and it is refused. 5fd08069
// and 89cb6289 run that filter (cell S1, Linux VM). That difference stays: it is
// the accepted edge of an old git.
//
// The call log of 5fd08069 shows two plain listings before its read-tree, both
// with --git-dir (cell F1). How that build tells the two sides apart is not
// measured.
//
// pin is the GIT_COMMON_DIR pin of baseRepo with the git directory itself as
// its value (commonDirPinEnv), in every state. So the listings of the rule read
// the repository's own config also in the state shadow, where the checkout
// reads the daemon's copy. A filter that only the repository's config names is
// in no copy, so the rule needs that config to give the filter its pairs. The
// scoped listing is claustrum's own call: 5fd08069 makes none (val-s3p2b1-linux
// row N4, Linux VM). In the state shadow the second plain listing of an old git
// is not the precursor of the checkout. The checkout runs its own listings
// against the copy.
//
// A listing that fails for another reason refuses the create with git's text,
// as a failed checkout. No filter runs then. That is claustrum's choice (not
// measured). One state is apart: ctx is done, by the timeoutMs of the caller.
// The plan is then empty, and the read-tree call cannot start under that ctx.
// So the caller answers as it did before the rule: the timeout arm, or the
// failed checkout with the text of the context. 5fd08069 makes no --show-scope
// call. So with a timeoutMs that expires while that call
// runs, it can answer the refusal of the rule where claustrum answers timeout
// (cell TF of round task010-02, Linux VM).
func listFilterPlan(ctx context.Context, u gitUse, leaf, gitDir string, pin []string) filterPlan {
	failed := func(r listingRun) filterPlan {
		if ctx.Err() != nil {
			return filterPlan{}
		}
		return filterPlan{refusal: worktreeGitText(r.stderr, r.err)}
	}
	env := precursorEnv(false, pin)
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+gitDir, "config", "-z", "--list", "--show-scope")
	cmd.Dir = leaf
	cmd.Env = env
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := gitOutput(ctx, cmd)
	var ee *exec.ExitError
	switch {
	case err == nil:
		full, outside := splitScopedListing(out)
		return planFromListings(full, outside, u.shadow())
	case !errors.As(err, &ee) || ee.ExitCode() != gitUsageExit:
		return failed(listingRun{stderr: errBuf.String(), err: err})
	}
	outside := runListing(ctx, leaf, "", outsideListingEnv())
	full := runListing(ctx, leaf, gitDir, env)
	for _, r := range []listingRun{outside, full} {
		if r.err != nil {
			return failed(r)
		}
	}
	f := planFromListings(full.out, outside.out, u.shadow())
	if !u.shadow() {
		f.listing = &full
	}
	return f
}

// refusedFilter is the name of the filter that failed the checkout, when it is
// one of the filters with the three pairs. git 2.43.0 ends its stderr with
// `fatal: <path>: smudge filter <name> failed`, with exit code 128, and so does
// Git for Windows (Windows VM). The first test is for a line that starts with
// "fatal:" and ends with "filter <name> failed". The longest such name counts,
// so a path that holds those words names no other filter. With no such line,
// the test is for "filter <name> failed" anywhere in stderr, and the first one
// in the text counts. So another wording of git still matches. With no match
// the caller answers the failed checkout as before.
//
// The line is English. The checkout call keeps the language settings of the
// daemon, so a translated git gives no match and the plain failed checkout. No
// filter runs there. That state is not measured.
func (f filterPlan) refusedFilter(stderr string) (string, bool) {
	for line := range strings.SplitSeq(stderr, "\n") {
		line = strings.TrimRight(line, "\r ")
		if !strings.HasPrefix(line, "fatal:") {
			continue
		}
		name, found := "", false
		for _, n := range f.refused {
			if strings.HasSuffix(line, "filter "+n+" failed") && (!found || len(n) > len(name)) {
				name, found = n, true
			}
		}
		if found {
			return name, true
		}
	}
	name, at := "", -1
	for _, n := range f.refused {
		if i := strings.Index(stderr, "filter "+n+" failed"); i >= 0 && (at < 0 || i < at) {
			name, at = n, i
		}
	}
	return name, at >= 0
}

// repositoryOnlyFilterText is the refusal text of errorCode repository_only_filter
// (cells A1, D4, D6, V9 and N1). The name is quoted with a backslash before a
// quote mark and before a backslash (cells D6 and Q11). A name with a letter
// outside ASCII stays as it is (cell Q11). Other bytes that strconv.Quote changes
// are not measured.
func repositoryOnlyFilterText(name string) string {
	q := strconv.Quote(name)
	return "Claude doesn't run the " + q + " git filter, because only this repository's own git config defines it." +
		" If you trust that filter, copy the [filter " + q + "] section from that config (usually .git/config)" +
		" into ~/.gitconfig on the remote machine, then try again."
}
