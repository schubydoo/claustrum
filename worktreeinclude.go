package main

import (
	"context"
	"errors"
	"math"
	"os/exec"
	"regexp"
	"strings"
)

// The directory scan of the `.worktreeinclude` copy.
//
// Every rule and every number in this file was measured against f6010b97.
// docs/PROTOCOL.md names the VMs that measured each rule.
//
// The scan has two forms. The old scan lists every ignored file that the
// manifest names. The new scan first lists the ignored entries of the repo. Then
// it asks git about the ignored files and about the directories that the manifest
// opens. A directory that no pattern opens is not searched.

const (
	// includeMaxPatterns is the number of counted patterns that can open a
	// directory. Pattern 256 opens its directory, and pattern 257 does not.
	includeMaxPatterns = 256

	// includeMaxPatternLen is the longest pattern, in bytes, that can open a
	// directory. The length is measured after one leading and one trailing `/`
	// are removed.
	includeMaxPatternLen = 1024

	// includeMaxSegments is the most `/`-separated segments a pattern can have
	// and still open a directory or set the any-depth flag.
	includeMaxSegments = 32

	// includeMaxDotDirs is the number of dot directories that the any-depth flag
	// can open. The root `.claude/` takes one of these places unless an
	// explicit pattern matches it.
	includeMaxDotDirs = 128

	// includeFallbackBytes is the size limit on the ignored files of the listing.
	// Each file costs its length plus includePathOverhead. Above this sum the old
	// scan runs. At exactly this sum the new scan runs.
	includeFallbackBytes = 1 << 20

	// gitArgvBudget (worktreeinclude_unix.go, worktreeinclude_windows.go) is
	// the budget of one batched `git ls-files` call. The fixed arguments after
	// the hardening options and the pathspecs share it. Each of them costs its
	// length plus includePathOverhead. See argvCost. The value differs by OS.

	// includePathOverhead is the per-argument cost in the fallback sum and in a
	// batch.
	includePathOverhead = 3

	// includeTempPrefix starts the name of the temp copy of the manifest. Its
	// length is 27 bytes, the same as the prefix in the measured f6010b97 argv.
	// The `--exclude-from` argument counts toward the batch budget. A
	// prefix of another length moves the batch split point by that many bytes.
	// os.CreateTemp adds a random decimal suffix, as f6010b97 does.
	includeTempPrefix = "claustrum-worktree-include-"
)

// includeSkipDotDirs are the dot-directory names that the any-depth flag never
// opens, at any depth. The match ignores case. A name on this list does not take
// one of the includeMaxDotDirs places. An explicit pattern still opens it.
//
// 165 names were tried, and only these 14 were skipped. Near names such as
// `.venv2` and `.cache-x` are not skipped.
var includeSkipDotDirs = map[string]bool{
	".angular":      true,
	".cache":        true,
	".dart_tool":    true,
	".gradle":       true,
	".next":         true,
	".nuxt":         true,
	".parcel-cache": true,
	".pnpm-store":   true,
	".svelte-kit":   true,
	".terraform":    true,
	".tox":          true,
	".turbo":        true,
	".venv":         true,
	".yarn":         true,
}

// gitSelectsIncludeScan runs `git version` and reports whether git is 2.32.0 or
// later. The caller runs it only when the manifest is a regular file. A
// non-zero exit selects the old scan, even when the output parses.
func gitSelectsIncludeScan() bool {
	ctx, cancel := gitCtx()
	defer cancel()
	out, err := gitVersionCmd(ctx).Output()
	if err != nil {
		return false
	}
	return versionSelectsIncludeScan(string(out))
}

// gitVersionCmd builds the `git version` call. Measured against f6010b97 on
// Linux and macOS VMs, it has no `-c` options and no `-C`, and no other git
// call runs before it. It runs in the daemon's own working directory, with the
// daemon's environment unchanged.
func gitVersionCmd(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "git", "version")
}

// versionSelectsIncludeScan parses `git version` output. The first
// `git version ` in the output counts, and text before it is allowed. One
// digit must follow it. Major and minor compare as numbers, and a number too
// large for an int counts as very large. A missing patch number is accepted,
// and text after the numbers is ignored. Output that does not parse selects
// the old scan.
func versionSelectsIncludeScan(out string) bool {
	_, rest, ok := strings.Cut(out, "git version ")
	if !ok {
		return false
	}
	major, rest, ok := leadingNumber(rest)
	if !ok || !strings.HasPrefix(rest, ".") {
		return false
	}
	minor, _, ok := leadingNumber(rest[1:])
	if !ok {
		return false
	}
	return major > 2 || (major == 2 && minor >= 32)
}

// leadingNumber splits the decimal digits at the start of s from the rest. A
// value too large for an int is math.MaxInt.
func leadingNumber(s string) (int, string, bool) {
	n, end := 0, 0
	for ; end < len(s) && s[end] >= '0' && s[end] <= '9'; end++ {
		d := int(s[end] - '0')
		if n > (math.MaxInt-d)/10 {
			n = math.MaxInt
		} else {
			n = n*10 + d
		}
	}
	if end == 0 {
		return 0, s, false
	}
	return n, s[end:], true
}

// includePlan is what the manifest contributes to the new scan.
type includePlan struct {
	openers []includeOpener
	// anyDepth is set by a counted, non-negated pattern of one segment without a
	// leading `/`, or by one that starts with `**/`. It opens the ignored dot
	// directories.
	anyDepth bool
}

// includeOpener is one pattern that can open ignored directories of the listing.
type includeOpener struct {
	// name is set for a literal name. It opens each listed directory that has
	// the name as one of its segments. It is lower case, because the match
	// ignores case.
	name string
	// first is set for one segment after a leading `/`. It opens each listed
	// directory whose first segment the segment matches as a glob.
	first *regexp.Regexp
	// prefix is set for a one-segment glob. It opens each listed directory whose
	// path starts with it. It is lower case, because the match ignores case.
	prefix string
	// segs is set for a pattern of two or more segments. A nil element is `**`.
	segs []*regexp.Regexp
	// deep is set when segs follow a leading `**/`. The match can then start at
	// any segment of the listed directory.
	deep bool
}

// parseIncludePlan reads the manifest the way the new scan does.
//
//   - A leading UTF-8 BOM, a trailing CR and unescaped trailing spaces are removed.
//   - Blank lines and `#` comments do not count. A line of only tabs and spaces
//     is blank. So is a line that is empty after one leading and one trailing
//     `/` are removed, such as `//`.
//   - A negated line counts but opens nothing.
//   - A `//` inside a line keeps an empty segment, which matches no name. So
//     `build//` and `build//a.txt` open the listed `build` and not `sub/build`.
//   - A line over the length cap or the segment cap does not count.
//   - Only the first includeMaxPatterns counted lines can open a directory.
//   - The any-depth flag ignores that count.
func parseIncludePlan(manifest []byte) includePlan {
	var plan includePlan
	counted := 0
	text := strings.TrimPrefix(string(manifest), "\ufeff")
	for _, line := range strings.Split(text, "\n") {
		line = trimTrailingSpaces(strings.TrimSuffix(line, "\r"))
		if strings.Trim(line, " \t") == "" || line[0] == '#' {
			continue
		}
		negated := line[0] == '!'
		body := strings.TrimPrefix(line, "!")
		anchored := strings.HasPrefix(body, "/")
		body = strings.TrimPrefix(body, "/")
		body = strings.TrimSuffix(body, "/")
		if body == "" {
			continue
		}
		if len(body) > includeMaxPatternLen {
			continue
		}
		segs := strings.Split(body, "/")
		if len(segs) > includeMaxSegments {
			continue
		}
		counted++
		if negated {
			continue
		}
		// A leading `/` still lets a one-segment pattern open a directory, but
		// it does not set the flag. A line with an empty segment sets no flag.
		if !hasEmptySegment(segs) && ((len(segs) == 1 && !anchored) || segs[0] == "**") {
			plan.anyDepth = true
		}
		if counted > includeMaxPatterns {
			continue
		}
		plan.openers = append(plan.openers, newIncludeOpeners(segs, anchored)...)
	}
	return plan
}

// trimTrailingSpaces removes trailing spaces, as gitignore does. A space after a
// backslash is kept, and so is a tab.
func trimTrailingSpaces(s string) string {
	end := len(s)
	for end > 0 && s[end-1] == ' ' {
		end--
	}
	if end == len(s) {
		return s
	}
	backslashes := 0
	for i := end - 1; i >= 0 && s[i] == '\\'; i-- {
		backslashes++
	}
	if backslashes%2 == 1 {
		end++
	}
	return s[:end]
}

// hasEmptySegment reports a `//` in the pattern. Such a pattern does not set
// the any-depth flag.
func hasEmptySegment(segs []string) bool {
	for _, s := range segs {
		if s == "" {
			return true
		}
	}
	return false
}

// isGlobByte reports the bytes that make a segment a glob.
func isGlobByte(b byte) bool {
	return b == '*' || b == '?' || b == '[' || b == '\\'
}

func hasGlob(s string) bool {
	for i := 0; i < len(s); i++ {
		if isGlobByte(s[i]) {
			return true
		}
	}
	return false
}

// newIncludeOpeners applies the shape rules. It returns no opener for a shape
// that opens no non-dot directory.
//
//   - one literal segment: each listed directory that has the name as any of
//     its segments, so `build` opens `build`, `sub/build` and `build/x`
//   - one segment after a leading `/`: each listed directory whose first
//     segment the segment matches as a glob. So `/build/` opens the root
//     `build` and not `sub/build`. `/a?b/` opens `aXb` and not `ab`. A `\`
//     escapes the next byte here, so `/ab\q/` opens nothing.
//   - one glob segment without a leading `/`: each listed directory whose
//     path starts with the literal prefix of the segment. The prefix ends
//     before the first `*`, `?`, `[` or `\`. So `a?b` opens `ab`, `a b` and `a/c`, and `sub\build` opens
//     `sub`, `sub/build` and `subbuild`. An empty prefix opens nothing.
//   - `**/` and then one literal: the same as the one literal segment
//   - `**/` and then a glob or `**`: nothing
//   - `**/` and then a literal and more segments: the rest of the pattern,
//     matched as below from any segment of the listed directory
//   - two or more segments: each listed directory that the pattern matches
//     segment by segment, as a prefix, and so its listed parents too. A
//     segment that ends in a lone `\` matches any name. So `sub\/x/` opens
//     each listed directory of one segment, and each listed `<name>/x`.
//
// A run of two `\` at the end of a segment is a literal `\`. A run of three
// acts like a run of one.
func newIncludeOpeners(segs []string, anchored bool) []includeOpener {
	deep := false
	switch {
	case len(segs) == 1 && !hasGlob(segs[0]) && !anchored:
		return []includeOpener{{name: strings.ToLower(segs[0])}}
	case len(segs) == 1 && anchored:
		first := compileSegmentGlob(segs[0])
		if first == nil {
			return nil
		}
		return []includeOpener{{first: first}}
	case len(segs) == 1:
		prefix := segs[0][:strings.IndexAny(segs[0], `*?[\`)]
		if prefix == "" {
			return nil
		}
		return []includeOpener{{prefix: strings.ToLower(prefix)}}
	case segs[0] == "**" && hasGlob(segs[1]):
		return nil
	case segs[0] == "**" && len(segs) == 2:
		return []includeOpener{{name: strings.ToLower(segs[1])}}
	case segs[0] == "**":
		segs, deep = segs[1:], true
	}
	res, ok := compileSegments(segs)
	if !ok {
		return nil
	}
	return []includeOpener{{segs: res, deep: deep}}
}

// includeAnyName matches any name. It stands for a segment that ends in a lone
// `\`.
var includeAnyName = regexp.MustCompile(`(?s)^.*$`)

// compileSegments compiles the segments of a pattern of two or more segments.
// A nil element is `**`. A segment that ends in a lone `\` matches any name.
// It reports false if another segment does not parse.
func compileSegments(segs []string) ([]*regexp.Regexp, bool) {
	res := make([]*regexp.Regexp, len(segs))
	for i, s := range segs {
		if s == "**" {
			continue
		}
		if endsInLoneBackslash(s) {
			res[i] = includeAnyName
			continue
		}
		if res[i] = compileSegmentGlob(s); res[i] == nil {
			return nil, false
		}
	}
	return res, true
}

// endsInLoneBackslash reports a segment that ends in an odd number of `\`
// bytes. Its last `\` escapes nothing.
func endsInLoneBackslash(s string) bool {
	n := len(s) - len(strings.TrimRight(s, `\`))
	return n%2 == 1
}

// opens reports whether the opener opens the listed directory dir. dir has no
// trailing `/`.
func (o includeOpener) opens(dir string) bool {
	entry := strings.Split(dir, "/")
	switch {
	case o.name != "":
		for _, seg := range entry {
			if strings.ToLower(seg) == o.name {
				return true
			}
		}
		return false
	case o.first != nil:
		return o.first.MatchString(entry[0])
	case o.prefix != "":
		return strings.HasPrefix(strings.ToLower(dir), o.prefix)
	case o.deep:
		for k := range entry {
			if prefixMatches(o.segs, entry[k:]) {
				return true
			}
		}
		return false
	}
	return prefixMatches(o.segs, entry)
}

// prefixMatches reports whether the pattern segments can match the entry
// segments as a prefix. The entry must run out first, or with the pattern.
func prefixMatches(pat []*regexp.Regexp, entry []string) bool {
	if len(entry) == 0 {
		return true
	}
	if len(pat) == 0 {
		return false
	}
	if pat[0] == nil {
		return prefixMatches(pat[1:], entry) || prefixMatches(pat, entry[1:])
	}
	return pat[0].MatchString(entry[0]) && prefixMatches(pat[1:], entry[1:])
}

// compileSegmentGlob turns one gitignore glob segment into an anchored regular
// expression that ignores case. It supports `*`, `?`, bracket classes with `!`
// or `^`, POSIX class names and backslash escapes. It returns nil for a glob that
// does not parse, such as an open bracket.
func compileSegmentGlob(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("(?is)^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '\\':
			i++
			if i == len(glob) {
				return nil
			}
			b.WriteString(regexp.QuoteMeta(glob[i : i+1]))
		case '[':
			end, class := bracketClass(glob, i)
			if end < 0 {
				return nil
			}
			b.WriteString(class)
			i = end
		default:
			b.WriteString(regexp.QuoteMeta(glob[i : i+1]))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}

// bracketClass converts the bracket expression that starts at glob[start]. It
// returns the index of the closing `]` and the regular expression class, or -1
// for an open bracket.
func bracketClass(glob string, start int) (int, string) {
	var b strings.Builder
	b.WriteString("[")
	i := start + 1
	if i < len(glob) && (glob[i] == '!' || glob[i] == '^') {
		b.WriteString("^")
		i++
	}
	for first := true; i < len(glob); first = false {
		c := glob[i]
		switch {
		case c == ']' && !first:
			b.WriteString("]")
			return i, b.String()
		case c == '[' && strings.HasPrefix(glob[i:], "[:"):
			end := strings.Index(glob[i+2:], ":]")
			if end < 0 {
				return -1, ""
			}
			b.WriteString(glob[i : i+2+end+2])
			i += 2 + end + 2
			continue
		case c == '\\' && i+1 < len(glob):
			i++
			c = glob[i]
		}
		if c == '\\' || c == ']' || c == '[' || c == '^' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
		i++
	}
	return -1, ""
}

// openIncludeDirs returns the listed directories that the new scan searches, in
// listing order. dirs have no trailing `/`. A dot directory that an explicit
// pattern matches takes none of the includeMaxDotDirs places.
//
// The root `.claude/` and `.claude/worktrees/` never open, even when a pattern
// names them. The root `.claude/` takes a place only when the any-depth flag
// reaches it and no explicit pattern matches it.
func openIncludeDirs(plan includePlan, dirs []string) []string {
	var open []string
	places := 0
	for _, dir := range dirs {
		if dir == claudeDirName+"/"+worktreesSubdir {
			continue
		}
		if plan.opensExplicitly(dir) {
			if dir != claudeDirName {
				open = append(open, dir)
			}
			continue
		}
		name := strings.ToLower(dir[strings.LastIndexByte(dir, '/')+1:])
		if plan.anyDepth && strings.HasPrefix(name, ".") && !includeSkipDotDirs[name] &&
			places < includeMaxDotDirs {
			places++
			if dir != claudeDirName {
				open = append(open, dir)
			}
		}
	}
	return open
}

func (p includePlan) opensExplicitly(dir string) bool {
	for _, o := range p.openers {
		if o.opens(dir) {
			return true
		}
	}
	return false
}

// includeFilesOverLimit reports whether the ignored files of the listing exceed
// the fallback limit. Directory entries do not count.
func includeFilesOverLimit(files []string) bool {
	total := 0
	for _, f := range files {
		total += len(f) + includePathOverhead
		if total > includeFallbackBytes {
			return true
		}
	}
	return false
}

// argvCost is the cost of args toward gitArgvBudget. Each argument costs its
// length plus includePathOverhead. This cost is a fit to the measured batch
// counts, not a value read from the reference.
func argvCost(args []string) int {
	cost := 0
	for _, a := range args {
		cost += len(a) + includePathOverhead
	}
	return cost
}

// batchIncludePathspecs splits paths into `git ls-files` calls. It fills one
// batch at a time in the given order. A batch holds as many paths as fit in
// limit. When the next path does not fit, a new batch starts. A path larger
// than the limit gets a batch of its own. That case was not measured.
func batchIncludePathspecs(paths []string, limit int) [][]string {
	var batches [][]string
	var cur []string
	size := 0
	for _, p := range paths {
		cost := len(p) + includePathOverhead
		if len(cur) > 0 && size+cost > limit {
			batches = append(batches, cur)
			cur, size = nil, 0
		}
		cur = append(cur, p)
		size += cost
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}

// splitNUL splits `-z` output into its non-empty entries.
func splitNUL(out string) []string {
	var entries []string
	for _, e := range strings.Split(out, "\x00") {
		if e != "" {
			entries = append(entries, e)
		}
	}
	return entries
}

// includeScanPlan is the git work of the new scan for one listing.
type includeScanPlan struct {
	// fileBatches hold the ignored files of the listing.
	fileBatches [][]string
	// dirBatches hold the opened directories. A directory pathspec has no
	// trailing `/`.
	dirBatches [][]string
	// fallback is set when the ignored files exceed the fallback limit.
	fallback bool
}

// includeBatchArgs is the fixed argv of one batch call of the scan, up to and
// including `--`. excludeArg is the `--exclude-from=<temp file>` argument.
func includeBatchArgs(excludeArg string) []string {
	return []string{"--literal-pathspecs", "ls-files", "-z", "--others", "--ignored", excludeArg, "--"}
}

// planIncludeScan splits the listing into files and directories and batches
// the pathspecs. Files and directories never share a batch. The directory
// batches start after the last file batch. excludeArg is the
// `--exclude-from=<temp file>` argument. The fixed argv of a batch call counts
// toward gitArgvBudget, and the pathspecs share the rest.
func planIncludeScan(manifest []byte, listing, excludeArg string) includeScanPlan {
	var files, dirs []string
	for _, e := range splitNUL(listing) {
		if d, ok := strings.CutSuffix(e, "/"); ok {
			dirs = append(dirs, d)
		} else {
			files = append(files, e)
		}
	}
	if includeFilesOverLimit(files) {
		return includeScanPlan{fallback: true}
	}
	limit := gitArgvBudget - argvCost(includeBatchArgs(excludeArg))
	return includeScanPlan{
		fileBatches: batchIncludePathspecs(files, limit),
		dirBatches:  batchIncludePathspecs(openIncludeDirs(parseIncludePlan(manifest), dirs), limit),
	}
}

// runIncludeBatches runs one `git ls-files` call per batch and joins the
// paths. A batch whose call fails is skipped, and the other batches still
// count.
func runIncludeBatches(repo, excludeArg string, batches [][]string) []string {
	var paths []string
	for _, batch := range batches {
		args := append(includeBatchArgs(excludeArg), batch...)
		if out, err := hardenedGitStdout(repo, false, args...); err == nil {
			paths = append(paths, splitNUL(out)...)
		}
	}
	return paths
}

// checkIgnored keeps the paths that the standard ignore rules match, with
// `git check-ignore --stdin -z`. Exit status 1 means that no path matched. Any
// other failure reports false.
func checkIgnored(repo string, paths []string) ([]string, bool) {
	out, err := hardenedGitStdin(repo, strings.Join(paths, "\x00")+"\x00",
		"check-ignore", "--stdin", "-z")
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return nil, false
	}
	return splitNUL(out), true
}

// scanWorktreeIncludes runs the new scan. It returns the repo-relative paths to
// copy. fallback is true when the ignored files exceed the fallback limit, and
// the caller then runs the old scan.
//
// excludeFile holds a copy of the manifest bytes.
//
// The file candidates are copied as the file batches name them. Only the paths
// from the directory batches go to check-ignore. If check-ignore fails, those
// paths are dropped and the file candidates are still copied.
func scanWorktreeIncludes(repo, excludeFile string, manifest []byte) (paths []string, fallback bool) {
	// The listing of ignored entries. An ignored directory is listed once, as
	// `dir/`. Parent directories that hold only ignored entries are listed too.
	listing, err := hardenedGitStdout(repo, false, "--no-literal-pathspecs", "ls-files", "-z",
		"--others", "--ignored", "--exclude-standard", "--directory",
		"--", ":(exclude)"+claudeDirName+"/"+worktreesSubdir)
	if err != nil {
		return nil, false
	}
	excludeArg := "--exclude-from=" + excludeFile
	plan := planIncludeScan(manifest, listing, excludeArg)
	if plan.fallback {
		return nil, true
	}
	paths = runIncludeBatches(repo, excludeArg, plan.fileBatches)
	fromDirs := runIncludeBatches(repo, excludeArg, plan.dirBatches)
	if len(fromDirs) == 0 {
		return paths, false
	}
	kept, _ := checkIgnored(repo, fromDirs)
	return append(paths, kept...), false
}
