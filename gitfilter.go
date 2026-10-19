package main

import (
	"bytes"
	"context"
	"maps"
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
	// lfsSkipSmudge passes under the name lfs with a settings copy, and the file
	// stays the pointer text (cell K3 of round task081-01, Linux VM). The other
	// --skip forms are not measured and do not pass.
	lfsSkipSmudge = "git-lfs smudge --skip %f"
	// lfsClean is the `clean` value of `git lfs install`. With a settings copy
	// the filter lfs gets it as an item (cell K1 of round task081-01). Another
	// `clean` value gets no item (not measured).
	lfsClean = "git-lfs clean -- %f"
	// lfsFailedWords and lfsDownloadNote: with a settings copy, a failed
	// checkout whose text holds the words after the cut gets the note after the
	// text (cells K6, K8, L1 to L3, L5, L9 and L11 of round task081-01, Linux VM).
	// A text that the cut ends before the words gets no note (cells K5 and K7).
	lfsFailedWords  = "smudge filter lfs failed"
	lfsDownloadNote = ". Claude doesn't use ssh, credential, or proxy settings from this repository's own git config." +
		" If the download needs one and you trust that setting, copy it into ~/.gitconfig on the remote machine, then try again."
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
//
// otherSmudge reports a `smudge` value in the listing, at any place, that is
// not empty, not `cat` and not a git-lfs value (lfsSmudge, lfsSkipSmudge).
// lfsSmudgeSeen reports a git-lfs value. With them the rule for a filter that
// only the repository defines does not depend on the order of its files
// (planFilters).
//
// bare reports a record of the key with no value, at any place: a line with
// no "=" (hasBare).
//
// clean is the last `clean` value. Only the filter lfs with a settings copy
// reads it (planFilters).
type filterValues struct {
	has           [len(filterKeys)]bool
	last          [len(filterKeys)]string
	bare          [len(filterKeys)]bool
	otherSmudge   bool
	lfsSmudgeSeen bool
	clean         string
}

// defined reports whether the listing defines the filter: a `smudge` or a
// `process` value that is not empty. A `clean` or a `required` key makes no
// definition (cells A3, A4, A8 and O4), and an empty `smudge` makes none (cells
// A5 and O5).
func (v filterValues) defined() bool {
	return v.last[filterSmudge] != "" || v.last[filterProcess] != ""
}

// hasBare reports a `smudge` or a `process` line with no "=" in v, the
// repository's own records (planFilters). Such a filter is refused as a value
// that does not pass (cells F5 and F6 of round task073-02, cell C3 of round
// task073-01, Linux VM). With a settings copy own holds the last `smudge`
// and the last `process` line of each file (lastLinePerFile). So a bare line
// that ends one file refuses, whatever another file of the repository says, in
// either order (cells G5a and G5b of round task073-07, macOS VM). This build
// gives the same in cells F5, F6, G5a and G5b of round task073-09 (Linux VM).
// The same line outside the repository
// makes no definition, and git fails with its own text (cell F11).
func (v filterValues) hasBare() bool {
	return v.bare[filterSmudge] || v.bare[filterProcess]
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
// A `smudge` or a `process` key with no value counts as empty, and bare marks
// it.
func parseFilterListing(out []byte) map[string]filterValues {
	defs := map[string]filterValues{}
	for rec := range bytes.SplitSeq(out, []byte{0}) {
		key, value, hasValue := strings.Cut(string(rec), "\n")
		rest, ok := strings.CutPrefix(key, "filter.")
		last := strings.LastIndexByte(rest, '.')
		if !ok || last < 0 {
			continue
		}
		if rest[last+1:] == "clean" {
			v := defs[rest[:last]]
			v.clean = value
			defs[rest[:last]] = v
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
		v.bare[k] = v.bare[k] || !hasValue
		if k == filterSmudge {
			lfs := value == lfsSmudge || value == lfsSkipSmudge
			v.otherSmudge = v.otherSmudge || (value != "" && value != passSmudge && !lfs)
			v.lfsSmudgeSeen = v.lfsSmudgeSeen || lfs
		}
		defs[rest[:last]] = v
	}
	return defs
}

// repositoryValuesPass reports whether the `smudge` and `process` values of a
// filter that only the repository defines are ones that pass. An empty value
// passes. With mixed values the filter is refused: `smudge = cat` beside a
// `process` value (cell V11), and an empty `process` beside a `smudge` value that
// does not pass (cell Q9). With lfsProgram set, the two git-lfs values do not
// pass (repositoryLfsProgramKey). With a settings copy lfsSkipSmudge passes
// under the name lfs too.
//
// With a settings copy (shadow) every `smudge` value of the listing counts,
// not only the last one. That listing holds the last value of each file of
// the repository (lastLinePerFile). 5fd08069 refuses a filter when a value
// in .git/config or in an includeIf file does not pass, whatever the order of
// the two files (cells F1e and F1f of round task084-05, F1g and F1h of round
// task084-06, Linux VM). In .git/config the last value counts (cells Q8a and
// Q8b of round task084-07).
func repositoryValuesPass(name string, v filterValues, lfsProgram, shadow bool) bool {
	lfs := name == lfsFilterName && !lfsProgram
	if shadow && (v.otherSmudge || (v.lfsSmudgeSeen && !lfs)) {
		return false
	}
	if s := v.last[filterSmudge]; s != "" && s != passSmudge && (!lfs || (s != lfsSmudge && (!shadow || s != lfsSkipSmudge))) {
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
//     (repositoryLfsProgramKey). With a settings copy and a git-lfs value it
//     gets an item for its `clean`, `process`, `required` and git-lfs `smudge`
//     values, in that order. The cells are K1 and K3 of round task081-01 (Linux
//     VM). The `clean` item needs lfsClean.
//   - A name with "=" is refused with no checkout (cell Q3). git ends the key of
//     a -c pair at the first "=". Cell Q3 has a value that does not pass. Such a
//     name with a value that passes is not measured.
//
// One filter that outside defines does not run: the filter lfs, when lfsKey is
// set. That needs a rule with no settings copy. It gets the three pairs too. A failure at it answers lfsProgramText, not
// the refusal of a filter that only the repository defines. With no path under
// the filter the checkout succeeds. A Windows VM ran this rule for the
// lfs.customtransfer keys and the standalone keys (cells L1 to L8).
//
// With a settings copy an outside `process = cat` makes no definition outside.
// The filter is then judged by own alone, the repository's own records, as a
// filter that only the repository defines. A repository `smudge` command is
// refused with the three pairs (cell F4 of round task073-02, Linux VM), also
// one that repeats the user value (cell G4 of round task073-07, macOS VM).
// `smudge = cat` or `required = true` alone succeeds (cells F20 and F21 of
// round task073-06, Linux VM). Another outside `process` value, and the state
// with no copy, are not measured: there the filter keeps the outside values.
//
// Every other filter that outside defines runs with the outside values, as on
// 5fd08069 in cells A7, O6 and Q6 (the `smudge` key). With a settings copy git
// does not see the values of the repository, so the filter gets no pair. That
// holds for a `process` key of the repository too, whose program does not run
// (cell F1 of round task073-02, Linux VM). It also holds for a `required` key
// of the repository beside one of the user config (cell F19). One state gets
// the `required` pair of the repository (repositoryRequiresUserSmudge).
//
// With no settings copy git reads the values of the repository, so the filter
// gets pairs with the outside values. One state gets no pair there
// (repositoryRequiresUserSmudge, claustrum's own, not measured). Two states get claustrum's own refusal,
// because no -c pair gives the result (unreplaceableFilterText). The first is a
// `process` key in full and none in outside: a pair cannot remove a key, and an
// empty `process` value stops the outside `smudge` filter too. The second is a
// name with "=" or a name that is not valid UTF-8. Such a name in the state of
// repositoryRequiresUserSmudge gets that refusal with a settings copy too.
// These states are claustrum's own, not measured.
//
// A name that is not valid UTF-8 cannot go through a Windows command line as it
// is, so a pair with it names another filter there. With a settings copy such
// a filter that only the repository defines, with a value that does not pass,
// gets its three pairs with the raw name. Its name is not in refused, so a failure at it answers git's text with
// the bytes that are not valid UTF-8 dropped (cells F12 and F13 of round
// task073-02, cells C4a and C4b of round task073-01, Linux VM). On Windows the
// create succeeds and no filter runs, as 5fd08069 does in cell X1 of round
// val20-windows (cells F12 and F13 of round task073-06, Windows VM). With no
// settings copy such a filter gets unreplaceableFilterText (claustrum's own, not
// measured).
//
// own holds the repository's own records. shadow is true in the state shadow
// with a settings copy.
func planFilters(full, outside, own map[string]filterValues, lfsKey string, shadow bool) filterPlan {
	var f filterPlan
	for _, name := range slices.Sorted(maps.Keys(full)) {
		v, o := full[name], outside[name]
		if shadow && o.defined() && o.last[filterProcess] == passSmudge {
			v, o = own[name], filterValues{}
		}
		bare := own[name].hasBare()
		switch {
		case o.defined() && name == lfsFilterName && lfsKey != "":
			// The repository names a program for git-lfs. The filter lfs of
			// the outside config does not run for this checkout.
			f.pairs(name, [...]string{"", "true", "false"}, filterProcess, filterRequired, filterSmudge)
			f.refused = append(f.refused, name)
			f.lfsKey = lfsKey
		case o.defined():
			if v.has == o.has && v.last == o.last {
				continue
			}
			userSmudge := repositoryRequiresUserSmudge(v, o)
			if shadow && !userSmudge {
				continue
			}
			if (v.has[filterProcess] && !o.has[filterProcess]) || strings.Contains(name, "=") || !utf8.ValidString(name) {
				f.refusal = unreplaceableFilterText(name)
				return f
			}
			if userSmudge {
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
		case !v.defined() && !bare:
		case strings.Contains(name, "="):
			f.refusal = "this repository's git config cannot be used: the filter " + strconv.Quote(name) +
				" is defined only there, and Claude cannot handle \"=\" in a filter's name; define it in ~/.gitconfig on the remote machine, or rename it"
			return f
		case !shadow && !utf8.ValidString(name):
			f.refusal = unreplaceableFilterText(name)
			return f
		case bare || !repositoryValuesPass(name, v, lfsKey != "", shadow):
			if len(name) > filterNameMaxBytes {
				f.refusal = "this repository's git config cannot be used: a filter defined only there has a name longer than " +
					strconv.Itoa(filterNameMaxBytes) + " bytes (it starts " + strconv.Quote(name[:filterNameStartBytes]) +
					"); define it in ~/.gitconfig on the remote machine, or rename it"
				return f
			}
			f.pairs(name, [...]string{"", "true", "false"}, filterProcess, filterRequired, filterSmudge)
			if utf8.ValidString(name) {
				f.refused = append(f.refused, name)
			}
		case shadow && name == lfsFilterName && (v.last[filterProcess] != "" ||
			v.last[filterSmudge] == lfsSmudge || v.last[filterSmudge] == lfsSkipSmudge):
			// Three states of this arm are claustrum's own, not measured. A
			// `required` value other than true goes as it is. A `clean` value of
			// the user config gives the `clean` item. A --skip smudge in one
			// repository file beside another git-lfs smudge in another file passes.
			if v.clean == lfsClean {
				f.args = append(f.args, "-c", "filter."+name+".clean="+lfsClean)
			}
			if v.last[filterProcess] != "" {
				f.pairs(name, v.last, filterProcess)
			}
			if v.has[filterRequired] {
				f.pairs(name, v.last, filterRequired)
			}
			if s := v.last[filterSmudge]; s == lfsSmudge || s == lfsSkipSmudge {
				f.pairs(name, v.last, filterSmudge)
			}
		case v.last[filterProcess] != "":
			f.pairs(name, v.last, filterProcess)
			if v.last[filterSmudge] == lfsSmudge {
				f.pairs(name, v.last, filterSmudge)
			}
		case v.last[filterSmudge] == lfsSmudge:
			f.pairs(name, v.last, filterSmudge)
		case shadow:
			// Every `smudge` value is `cat`. With a settings copy the read-tree
			// call gets no item for the filter, and git does not see it in the
			// copy. 5fd08069 passes no item there and succeeds (cell F1i of
			// round task084-06, Linux VM).
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
// them a command. full and outside are the two plain
// listings. A record that outside has too is not the repository's own, so the
// same keys in the user or the system config do not count.
//
// A working git-lfs 3.7.1 runs the program of lfs.customtransfer.<x>.path on
// 89cb6289 and on build C of this change, and it does not run on
// 5fd08069 (cell X4, Windows VM). So with one of these keys and no settings
// copy the filter lfs does not run for the checkout, wherever it is defined
// (planFilters). That rule is claustrum's own, not measured. With build D the
// agent ran in the same cells as on 5fd08069: with the keys in the user config
// only, and with shadowDirOff (cells L1 to L8, Windows VM).
//
// With a settings copy the keys refuse nothing (settingRecords). git-lfs runs,
// and the program of the key does not run, as on 5fd08069. The cells are K5,
// K6, L2 to L4 and L11 of round task081-01 (Linux VM).
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
// the checkout reads, and the part of it from outside the repository. With a
// settings copy the repository part holds no `lfs.` key (settingRecords), so
// the git-lfs program key is "".
func planFromListings(full, outside []byte, shadow bool) filterPlan {
	f := planFilters(parseFilterListing(full), parseFilterListing(outside), parseFilterListing(ownRecords(full, outside)),
		repositoryLfsProgramKey(full, outside), shadow)
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
	names := map[string]bool{}
	for rec := range bytes.SplitSeq(ownRecords(full, outside), []byte{0}) {
		key, _, _ := strings.Cut(string(rec), "\n")
		rest, ok := strings.CutPrefix(key, "filter.")
		if last := strings.LastIndexByte(rest, '.'); ok && last >= 0 {
			names[rest[:last]] = true
		}
	}
	return len(names)
}

// ownRecords is the listing of the records of full that outside does not
// hold, with each record of outside taken once: the repository's own records.
func ownRecords(full, outside []byte) []byte {
	other := map[string]int{}
	for rec := range bytes.SplitSeq(outside, []byte{0}) {
		other[string(rec)]++
	}
	var own []byte
	for rec := range bytes.SplitSeq(full, []byte{0}) {
		if other[string(rec)] > 0 {
			other[string(rec)]--
			continue
		}
		own = append(append(own, rec...), 0)
	}
	return own
}

// lfsProgramText is the refusal text when the checkout fails at the filter lfs
// that the outside config defines, and key is the program key of the
// repository. Only a rule with no settings copy gives it. The text is
// claustrum's own.
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

// filterRecords is the part of a plain listing that the filter rule reads: the
// records whose key starts with `filter.` or `lfs.`.
func filterRecords(listing []byte) []byte {
	var b []byte
	for rec := range bytes.SplitSeq(listing, []byte{0}) {
		if bytes.HasPrefix(rec, []byte("filter.")) || bytes.HasPrefix(rec, []byte("lfs.")) {
			b = append(append(b, rec...), 0)
		}
	}
	return b
}

// settingRecords is the plain listing of settings, in the form of `git config
// -z --list`, cut to the records whose key starts with `filter.`. The rule with
// a settings copy reads no `lfs.` key of the repository.
func settingRecords(settings ...[]repoSetting) []byte {
	var b []byte
	for _, list := range settings {
		for _, s := range list {
			if !strings.HasPrefix(s.key, "filter.") {
				continue
			}
			b = append(b, s.key...)
			if s.hasValue {
				b = append(append(b, '\n'), s.value...)
			}
			b = append(b, 0)
		}
	}
	return b
}

// shadowListings gives the two listings of planFromListings for a create with
// a settings copy, with no git call of its own. 5fd08069 makes no call with
// --show-scope there (round task084-01, cells R-Q1a, R-Q2b, R-Q2c, R-Q4a,
// R-Q4b and R-Q4h, Linux, macOS and Windows VMs). How claustrum tells the two
// sides apart is its own.
//
// pin is the listing before the read-tree call (hardenedGitCheckout). It reads
// the copy, which holds no key that the rule reads, and no `config.worktree`
// file of the repository (extensions.worktreeconfig is never in the copy). So
// every record of pin is from outside the repository, and outside is pin. own
// is the settingRecords of the read of repoConfigGate that wrote the copy of
// this request: the config, the files that it includes, the files of its
// `includeIf` sections and its `config.worktree` file. 5fd08069 refuses a
// filter of an included file (cell F1 of round task084-02, Linux VM) and of an
// `includeIf` file (cell F1b). full is outside and then own, so the values of
// the repository come after those of GIT_CONFIG_COUNT (claustrum's own, not
// measured).
//
// A filter that the repository config gets before the create gets a new read
// and is refused (cell F2). One that it gets after the read is in no copy, so
// git does not run it (claustrum's own, not measured).
func shadowListings(pin, own []byte) (full, outside []byte) {
	outside = filterRecords(pin)
	return append(slices.Clone(outside), own...), outside
}

// filterRule is the filter rule of one checkout. hardenedGitCheckout sets plan.
// A create in the state fallback-git has no rule: 5fd08069 adds no filter item
// there and refuses no filter (cells R-Q4f and R-Q4g of round task084-01, Linux
// VM). A create with "shadowDirOff":true has none either, as before.
//
// shadow is true for a create with a settings copy (req.repoRead set), and own
// then holds the records of its read (shadowListings). Every other create with
// a rule takes the scoped listing (scopedPlan).
type filterRule struct {
	shadow bool
	own    []byte
	plan   filterPlan
}

// planFor sets the plan of a rule with a settings copy from pin, the output of
// the listing before the read-tree call.
func (r *filterRule) planFor(pin []byte) {
	full, outside := shadowListings(pin, r.own)
	r.plan = planFromListings(full, outside, true)
}

// scopedPlan sets the plan of a rule with no settings copy: a server with no
// run folder, or a read of the repository config with no verdict. Such a
// create reads the live config of the repository, so the rule needs both sides
// at the moment of the checkout. It runs `--git-dir=<gitDir> config -z --list
// --show-scope` in leaf, with the light precursorEnv and pin, under ctx
// (splitScopedListing). That call and its rules are claustrum's own, not
// measured: no cell of rounds task084-01 and task084-02 is in such a state. A
// listing that fails refuses the create with git's text. So a git before 2.26,
// which has no --show-scope, refuses every create there. With ctx done the plan
// is empty, and the read-tree call cannot start under ctx.
func (r *filterRule) scopedPlan(ctx context.Context, leaf, gitDir string, pin []string) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+gitDir, "config", "-z", "--list", "--show-scope")
	cmd.Dir = leaf
	cmd.Env = precursorEnv(false, pin)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := gitOutput(ctx, cmd)
	switch {
	case err == nil:
		full, outside := splitScopedListing(out)
		r.plan = planFromListings(full, outside, false)
	case ctx.Err() != nil:
		r.plan = filterPlan{}
	default:
		r.plan = filterPlan{refusal: worktreeGitText(errBuf.String(), err)}
	}
}

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
