package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Doc tests. Each test reads the real documents of the repository and fails when
// a fact that the documents copy from the code no longer matches the code. Every
// failure message names the file to change and what to put there.
//
// The tests read files with paths built by filepath.Join from the package
// directory, split on "\n" after a replace of "\r\n", and do not depend on the
// order of the files.

// docRead returns one repository file with "\r\n" turned into "\n".
func docRead(t *testing.T, elem ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(elem...))
	if err != nil {
		t.Fatalf("cannot read %s: %v", filepath.Join(elem...), err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// goSourceFiles returns the .go files of the package directory and of each
// package under internal/, test files too. A testdata folder is skipped.
func goSourceFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no .go files found in the package directory: %v", err)
	}
	err = filepath.WalkDir("internal", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cannot walk internal: %v", err)
	}
	return files
}

// TestDocsGoSourceFilesSeeInternal: the doc tests read the files under internal/ too,
// so a method name or an error code in a moved file stays checked.
func TestDocsGoSourceFilesSeeInternal(t *testing.T) {
	want := filepath.Join("internal", "renameretry", "renameretry.go")
	if !slices.Contains(goSourceFiles(t), want) {
		t.Errorf("goSourceFiles misses %s", want)
	}
}

// docFlat folds every run of white space into one space, so that a sentence that
// the document wraps over several lines matches one pattern.
func docFlat(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// docFiles lists the documents that the doc tests scan, as paths with "/" slashes.
func docFiles(t *testing.T) []string {
	t.Helper()
	files := []string{"README.md", "AGENTS.md", "SECURITY.md", "CONTRIBUTING.md"}
	for _, pat := range []string{"docs/*.md", "docs/protocol/*.md"} {
		m, err := filepath.Glob(filepath.FromSlash(pat))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range m {
			files = append(files, filepath.ToSlash(f))
		}
	}
	sort.Strings(files)
	return files
}

func docRel(rel string) string { return filepath.FromSlash(rel) }

var docFenceRE = regexp.MustCompile("(?ms)^```.*?^```")
var docSpanRE = regexp.MustCompile("`([^`]+)`")

// docSpans returns every inline code span of a document. Fenced blocks are dropped
// first, because a fence would pair its backticks with the wrong ones. Spans pair
// inside one paragraph only, so a stray backtick cannot shift every later span.
func docSpans(s string) []string {
	s = docFenceRE.ReplaceAllString(s, "")
	var out []string
	for _, para := range strings.Split(s, "\n\n") {
		for _, m := range docSpanRE.FindAllStringSubmatch(para, -1) {
			out = append(out, strings.TrimSpace(m[1]))
		}
	}
	return out
}

func docSetDiff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// docMethods is the method list that the documents show: the Linux and macOS list.
// On Windows capabilityMethods has no files.find and no files.mkdir, so they go
// back before files.realpath.
func docMethods() []string {
	if slices.Contains(capabilityMethods, "files.mkdir") {
		return capabilityMethods
	}
	last := len(capabilityMethods) - 1
	return append(capabilityMethods[:last:last], "files.find", "files.mkdir", capabilityMethods[last])
}

// docMethodRowRE finds a row of the method table of docs/PROTOCOL.md: the method in
// group 1, and the page that its last cell links to in group 2.
var docMethodRowRE = regexp.MustCompile("(?m)^\\| `([A-Za-z_.]+)` \\|.*\\]\\(protocol/([a-z0-9-]+\\.md)(?:#[a-z0-9_-]+)?\\) \\|$")

// TestDocsEveryMethodHasAHome: each method of server.capabilities has a heading in
// docs/PROTOCOL.md, a page in docs/protocol/, or a row of the method table of
// docs/PROTOCOL.md that links to a page that exists.
func TestDocsEveryMethodHasAHome(t *testing.T) {
	named := map[string]bool{}
	text := docRead(t, "docs", "PROTOCOL.md")
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "##") {
			// A heading names the method as its first word, with or without backticks.
			if f := strings.Fields(strings.TrimLeft(line, "#")); len(f) > 0 {
				named[strings.Trim(f[0], "`")] = true
			}
		}
	}
	var rows []string
	for _, m := range docMethodRowRE.FindAllStringSubmatch(text, -1) {
		if _, err := os.Stat(filepath.Join("docs", "protocol", m[2])); err == nil {
			named[m[1]] = true
		}
		rows = append(rows, m[1])
	}
	if !slices.Equal(rows, docMethods()) {
		t.Errorf("the method table of docs/PROTOCOL.md is %v, and server.capabilities returns %v: put the rows in the order of capabilityMethods", rows, docMethods())
	}
	for _, m := range docMethods() {
		page := strings.NewReplacer(".", "-", "_", "-").Replace(m)
		_, err1 := os.Stat(filepath.Join("docs", "protocol", page+".md"))
		_, err2 := os.Stat(filepath.Join("docs", "protocol", strings.ToLower(page)+".md"))
		if named[m] || err1 == nil || err2 == nil {
			continue
		}
		t.Errorf("docs/PROTOCOL.md has no section for the method %q: add a row for it to the method table of docs/PROTOCOL.md with a link to its page, or a page docs/protocol/%s.md", m, page)
	}
}

// docNamespaceRE finds a span that names something in a namespace of the daemon.
var docNamespaceRE = regexp.MustCompile(`^(?:server|files|git|launcher|process|plugins)\.[A-Za-z0-9_.*]+$`)

// docNotMethods lists the spans of the form <namespace>.<name> that are not
// methods and not advertised features. Each entry has its reason.
var docNotMethods = map[string]string{
	"server.version":     "the documents name it as the method that 7d193f89 removed",
	"server.go":          "a source file",
	"process.go":         "a source file",
	"git.exe":            "the Windows git program, inside an error text",
	"files.PluginHashRE": "a Go name of the package internal/files",
	// The terminal of 683cc6a9, which claustrum does not build.
	"process.resize":    "a method of the reference build 683cc6a9 that claustrum lacks",
	"process.spawn.pty": "a feature of the reference build 683cc6a9 that claustrum lacks",
}

// TestDocsNoPhantomMethod: a span like `git.<name>` in the documents is a method, an
// advertised feature, a wildcard like `git.*`, or an entry of docNotMethods.
func TestDocsNoPhantomMethod(t *testing.T) {
	known := map[string]bool{}
	for _, m := range docMethods() {
		known[m] = true
	}
	for _, f := range capabilityFeatures {
		known[f] = true
	}
	// capabilityFeatures lists these on unix only, and the test must pass on Windows.
	known["files.mkdir.verified_parent"] = true
	known["git.worktree.external_root"] = true
	known["process.spawn.link_bound"] = true
	known["git.worktree_create.own_lfs_store"] = true
	known["git.worktree_create.lfs_error_codes"] = true
	known["process.spawn.cli_temp_root"] = true
	known["process.spawn.cli_temp_root_end"] = true
	// It lists this one on Windows only.
	known["files.realpath.how_far"] = true
	// It lists this one on Linux and macOS only.
	known["launcher.exit_ends_group"] = true
	// It lists these on Linux only.
	known["launcher.checked_by_handle"] = true
	known["launcher.local_kinds_only"] = true
	// featuresFor adds one of these on a daemon with jobs on (Windows).
	known[jobFeature] = true
	known[jobPendingFeature] = true
	known[jobUnavailableFeature] = true
	for _, f := range docFiles(t) {
		for _, span := range docSpans(docRead(t, docRel(f))) {
			if !docNamespaceRE.MatchString(span) || span == "" {
				continue
			}
			if strings.HasSuffix(span, ".*") && !strings.Contains(strings.TrimSuffix(span, ".*"), "*") &&
				strings.Count(span, ".") == 1 {
				continue
			}
			if known[span] {
				continue
			}
			if _, ok := docNotMethods[span]; ok {
				continue
			}
			t.Errorf("%s names `%s`, which is no method and no advertised feature: replace it with a real name, or add it with a reason to docNotMethods in docs_consistency_test.go", f, span)
		}
	}
	for name := range docNotMethods {
		if known[name] {
			t.Errorf("docNotMethods lists %q, which is now a method or a feature: delete the entry", name)
		}
	}
}

// docGoNameRE finds <namespace>.<name> words in Go comments and flag usage strings.
var docGoNameRE = regexp.MustCompile(`\b(?:server|files|git|launcher|process|plugins)\.[a-z][A-Za-z_]*(?:\.[A-Za-z_]+)*\b\*?`)

// docGoNotMethods lists the words of Go text that look like a method and are not.
// A word that ends in ".go", ".exe" or "*", or holds ".log", is a file name or a
// glob and needs no entry.
var docGoNotMethods = map[string]string{
	"git.list_branch":                "a deliberate typo that a comment of shutdown_auth_test.go gives as an example",
	"server.capabilities.instanceId": "the instanceId member of the server.capabilities result, named in a comment of pipetransport.go",
	"server.run":                     "the run method of the server type, named in comments of pipetransport_other.go and pipetransport_windows.go",
}

// TestDocsNoPhantomMethodInGoText: the `//` comments of every .go file of the
// package, and the usage strings of the flag definitions in the non-test files,
// name only real methods or advertised features. The test reads each line from
// its first `//` to the end. It reads no other text of a test file, because a
// test frame may use any method name.
func TestDocsNoPhantomMethodInGoText(t *testing.T) {
	known := map[string]bool{"files.realpath.how_far": true, "launcher.exit_ends_group": true, "files.mkdir.verified_parent": true, "git.worktree.external_root": true, "process.spawn.link_bound": true, "git.worktree_create.own_lfs_store": true, "git.worktree_create.lfs_error_codes": true, "process.spawn.cli_temp_root": true, "process.spawn.cli_temp_root_end": true, "launcher.checked_by_handle": true, "launcher.local_kinds_only": true}
	for _, m := range docMethods() {
		known[m] = true
	}
	for _, f := range capabilityFeatures {
		known[f] = true
	}
	known[jobFeature] = true
	known[jobPendingFeature] = true
	known[jobUnavailableFeature] = true
	files := goSourceFiles(t)
	flagDefRE := regexp.MustCompile(`flag\.[A-Za-z0-9]+\(`)
	check := func(file string, n int, text string) {
		for _, name := range docGoNameRE.FindAllString(text, -1) {
			if known[name] || strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".exe") || strings.Contains(name, ".log") || strings.HasSuffix(name, "*") {
				continue
			}
			if _, ok := docGoNotMethods[name]; ok {
				continue
			}
			t.Errorf("%s line %d names `%s`, which is no method and no advertised feature: name a real method", file, n, name)
		}
	}
	for _, file := range files {
		isTest := strings.HasSuffix(file, "_test.go")
		for i, line := range strings.Split(docRead(t, file), "\n") {
			if idx := strings.Index(line, "//"); idx >= 0 {
				check(file, i+1, line[idx:])
			}
			if !isTest && flagDefRE.MatchString(line) {
				check(file, i+1, line)
			}
		}
	}
}

// docCountSentence is one place where a document states the present number of
// methods. re finds the sentence in the white-space-folded text and captures the
// number in group 1. A reworded sentence must be fixed here too.
type docCountSentence struct {
	file string
	re   *regexp.Regexp
}

var docMethodCountSentences = []docCountSentence{
	{"README.md", regexp.MustCompile(`The daemon has (\d+) methods`)},
	{"AGENTS.md", regexp.MustCompile(`the (\d+) methods across`)},
	{"docs/index.md", regexp.MustCompile(`The daemon supplies (\d+) methods`)},
	{"docs/ARCHITECTURE.md", regexp.MustCompile(`the (\d+) method handlers`)},
	{"docs/PROTOCOL.md", regexp.MustCompile(`## Methods \((\d+)\)`)},
	{"docs/PROTOCOL.md", regexp.MustCompile(`"methods":\[…(\d+)…\]`)},
}

// TestDocsMethodCount: the documents state the present method count, and it equals
// len(capabilityMethods). The history sentences of
// REFERENCE-BUILDS.md are not in the list on purpose.
func TestDocsMethodCount(t *testing.T) {
	want := len(docMethods())
	for _, s := range docMethodCountSentences {
		m := s.re.FindStringSubmatch(docFlat(docRead(t, docRel(s.file))))
		if m == nil {
			t.Errorf("%s no longer holds the sentence that matches %s: reword it back, or update docMethodCountSentences in docs_consistency_test.go", s.file, s.re)
			continue
		}
		if m[1] != strconv.Itoa(want) {
			t.Errorf("%s says %s methods (%q), but the daemon has %d: change the number to %d", s.file, m[1], m[0], want, want)
		}
	}
}

var (
	docCatalogRowRE = regexp.MustCompile(`(?m)^\| \[(D\d+|CT-\d+)\]\(#([a-z0-9-]+)\)`)
	docEntryRE      = regexp.MustCompile(`(?m)^### (D\d+|CT-\d+) · .*$`)
	docAnchorRE     = regexp.MustCompile(`\{ #([a-z0-9-]+) \}$`)
	docAgentsRowRE  = regexp.MustCompile(`(?m)^\| (D\d+|CT-\d+) \|`)
)

// docSection returns the text from the line "## <title>" to the next "## " line.
func docSection(t *testing.T, text, title string) string {
	t.Helper()
	start := strings.Index(text, "\n## "+title+"\n")
	if start < 0 {
		t.Fatalf("docs/DIVERGENCES.md has no \"## %s\" heading: restore it, or update docSection users in docs_consistency_test.go", title)
	}
	rest := text[start+1:]
	if end := strings.Index(rest[3:], "\n## "); end >= 0 {
		rest = rest[:end+3]
	}
	return rest
}

// TestDocsDivergenceCatalog: the table of the catalog, the entry headings and the
// Part B table of AGENTS.md list the same live IDs, every heading carries its
// anchor, and a retired entry is no row of the table.
func TestDocsDivergenceCatalog(t *testing.T) {
	text := docRead(t, "docs", "DIVERGENCES.md")
	catalog := map[string]bool{}
	for _, m := range docCatalogRowRE.FindAllStringSubmatch(docSection(t, text, "Catalog"), -1) {
		catalog[m[1]] = true
		if m[2] != strings.ToLower(m[1]) {
			t.Errorf("docs/DIVERGENCES.md catalog row %s links to #%s: change the link to #%s", m[1], m[2], strings.ToLower(m[1]))
		}
	}
	headings := func(section string, label string) map[string]bool {
		set := map[string]bool{}
		for _, h := range docEntryRE.FindAllStringSubmatch(section, -1) {
			set[h[1]] = true
			a := docAnchorRE.FindStringSubmatch(h[0])
			if a == nil || a[1] != strings.ToLower(h[1]) {
				t.Errorf("docs/DIVERGENCES.md %s heading %q has no anchor { #%s }: add it at the end of the line", label, h[0], strings.ToLower(h[1]))
			}
		}
		return set
	}
	live := headings(docSection(t, text, "Entries"), "entry")
	retired := headings(docSection(t, text, "Retired entries"), "retired")
	if len(catalog) == 0 || len(live) == 0 || len(retired) == 0 {
		t.Fatalf("docs/DIVERGENCES.md: found %d catalog rows, %d entries and %d retired entries: the layout changed, update docs_consistency_test.go", len(catalog), len(live), len(retired))
	}
	for _, id := range docSetDiff(live, catalog) {
		t.Errorf("docs/DIVERGENCES.md has the entry %s but no row for it in the catalog table: add a row", id)
	}
	for _, id := range docSetDiff(catalog, live) {
		t.Errorf("docs/DIVERGENCES.md has a catalog row for %s but no \"### %s ·\" entry under \"## Entries\": add the entry, or delete the row", id, id)
	}
	for id := range retired {
		if catalog[id] || live[id] {
			t.Errorf("docs/DIVERGENCES.md lists %s as retired and as live: keep it in one place", id)
		}
	}
	agents := map[string]bool{}
	for _, m := range docAgentsRowRE.FindAllStringSubmatch(docSection(t, docRead(t, "AGENTS.md"), "Gotchas — Part B: the live divergences"), -1) {
		agents[m[1]] = true
	}
	for _, id := range docSetDiff(live, agents) {
		t.Errorf("AGENTS.md Part B has no row for the live entry %s of docs/DIVERGENCES.md: add a row", id)
	}
	for _, id := range docSetDiff(agents, live) {
		t.Errorf("AGENTS.md Part B has a row for %s, which is no live entry of docs/DIVERGENCES.md: delete the row", id)
	}
}

var (
	docDRE  = regexp.MustCompile(`\bD([1-9][0-9]*)\b`)
	docCTRE = regexp.MustCompile(`\bCT-([1-9][0-9]*)\b`)
)

// TestDocsDivergenceIDsExist: every D<n> and CT-<n> that a document cites is a live
// or retired entry of docs/DIVERGENCES.md. The pattern skips the cells of the
// measurement tables that carry a letter, a leading zero or a dash after the D.
// A bare cell such as row D1 of a table is read as the entry D1 and passes when
// that entry exists, so this test is blind to a measurement cell with a number
// that an entry also has.
func TestDocsDivergenceIDsExist(t *testing.T) {
	text := docRead(t, "docs", "DIVERGENCES.md")
	ids := map[string]bool{}
	for _, m := range docEntryRE.FindAllStringSubmatch(text, -1) {
		ids[m[1]] = true
	}
	if len(ids) == 0 {
		t.Fatal("docs/DIVERGENCES.md has no entry headings: update docs_consistency_test.go")
	}
	for _, f := range docFiles(t) {
		doc := docRead(t, docRel(f))
		for _, re := range []*regexp.Regexp{docDRE, docCTRE} {
			seen := map[string]bool{}
			for _, m := range re.FindAllString(doc, -1) {
				if !ids[m] && !seen[m] {
					seen[m] = true
					t.Errorf("%s cites %s, which is no entry of docs/DIVERGENCES.md: fix the citation, or add the entry", f, m)
				}
			}
		}
	}
}

var docNavRE = regexp.MustCompile(`(?m)^\s*-\s+[^:\n]+:\s+((?:https?://\S*/)?\S+\.md)\s*$`)

// TestDocsSiteListsEveryDocument: every docs/*.md and docs/protocol/*.md is in the
// nav of mkdocs.yml, every nav page exists, and docs/index.md links every top-level
// page of the nav.
func TestDocsSiteListsEveryDocument(t *testing.T) {
	nav := map[string]bool{}
	for _, m := range docNavRE.FindAllStringSubmatch(docRead(t, "mkdocs.yml"), -1) {
		nav[m[1]] = true
	}
	if len(nav) == 0 {
		t.Fatal("mkdocs.yml: no nav entry of the form \"- Title: page.md\" found: update docs_consistency_test.go")
	}
	index := docRead(t, "docs", "index.md")
	for _, f := range docFiles(t) {
		rel, ok := strings.CutPrefix(f, "docs/")
		if !ok || rel == "index.md" {
			continue
		}
		if !nav[rel] {
			t.Errorf("%s is not in the nav of mkdocs.yml: add a line \"- Title: %s\" under nav", f, rel)
		}
	}
	for page := range nav {
		if strings.HasPrefix(page, "http") {
			continue
		}
		if _, err := os.Stat(filepath.Join("docs", filepath.FromSlash(page))); err != nil {
			t.Errorf("mkdocs.yml nav names %s, which does not exist in docs/: fix the name or delete the line", page)
			continue
		}
		if page == "index.md" || strings.Contains(page, "/") {
			continue
		}
		if !regexp.MustCompile(`\]\(` + regexp.QuoteMeta(page) + `(?:#[^)]*)?\)`).MatchString(index) {
			t.Errorf("docs/index.md has no link to %s: add an entry for it to \"Where to go next\"", page)
		}
	}
}

// docFlagDefRE finds a flag name in both forms: flag.String("name", ...) and
// flag.StringVar(&x, "name", ...).
var docFlagDefRE = regexp.MustCompile(`flag\.[A-Za-z0-9]+\((?:[^,"]*,\s*)?"([a-z0-9-]+)"`)

// docExpectedFlagCount is the number of flags that main.go defines today. Update
// it when a flag is added or removed, and document the new flag in docs/protocol/flags.md
// (or in INSTALL.md for a flag of -install).
const docExpectedFlagCount = 27

// docUndocumentedFlags lists flags that the documents deliberately leave out.
var docUndocumentedFlags = map[string]string{}

// TestDocsEveryFlagIsDocumented: each flag that main.go defines appears as a code
// span `-name` in docs/protocol/flags.md, docs/INSTALL.md or README.md, alone or inside a longer span such
// as `-wire-log <path>`.
func TestDocsEveryFlagIsDocumented(t *testing.T) {
	defs := docFlagDefRE.FindAllStringSubmatch(docRead(t, "main.go"), -1)
	if len(defs) != docExpectedFlagCount {
		t.Fatalf("main.go defines %d flags by the pattern docFlagDefRE, but the test expects %d: update docExpectedFlagCount in docs_consistency_test.go (a flag was added or removed, or its definition no longer matches the pattern)", len(defs), docExpectedFlagCount)
	}
	var spans []string
	for _, f := range []string{"docs/protocol/flags.md", "docs/INSTALL.md", "README.md"} {
		spans = append(spans, docSpans(docRead(t, docRel(f)))...)
	}
	// A flag counts as named when a span holds a word "-name" or "--name", with an
	// optional "=value" after it.
	words := map[string]bool{}
	for _, sp := range spans {
		for _, w := range strings.FieldsFunc(sp, func(r rune) bool { return r == ' ' || r == '=' || r == '[' || r == ']' }) {
			words[strings.TrimLeft(w, "-")] = strings.HasPrefix(w, "-") || words[strings.TrimLeft(w, "-")]
		}
	}
	for _, d := range defs {
		name := d[1]
		if _, ok := docUndocumentedFlags[name]; ok {
			continue
		}
		if !words[name] {
			t.Errorf("main.go defines the flag -%s, but docs/protocol/flags.md, docs/INSTALL.md and README.md have no code span for it: document `-%s` in the flags table of docs/protocol/flags.md", name, name)
		}
	}
}

// docPinSentence is one place where a document states the current pinned build.
// re finds the sentence in the folded text and captures the hash in group 1.
type docPinSentence struct {
	file string
	re   *regexp.Regexp
}

var docPinSentences = []docPinSentence{
	{"AGENTS.md", regexp.MustCompile("Unmarked behavior follows the reference, pinned at `([0-9a-f]+)`")},
	{"docs/UPSTREAM-TRACKING.md", regexp.MustCompile("claustrum follows `([0-9a-f]+)`, the build that `scripts/UPSTREAM_SHA` names")},
	{"docs/UPSTREAM-TRACKING.md", regexp.MustCompile("The build pinned today, `([0-9a-f]+)`")},
	{"docs/UPSTREAM-TRACKING.md", regexp.MustCompile("the current baseline, `([0-9a-f]+)`")},
	{"docs/REFERENCE-BUILDS.md", regexp.MustCompile("\\| Reference SHA \\| Built \\(UTC\\) \\| Wire changes \\| Reconciled in \\| \\|---\\|---\\|---\\|---\\| \\| `([0-9a-f]+)…`")},
}

// TestDocsPinnedBuild: each sentence of docPinSentences names the build that
// scripts/UPSTREAM_SHA holds. Other sentences that name the pin are not tested.
// A ledger hash is the short form
// of the pin, so the pin has to start with it.
func TestDocsPinnedBuild(t *testing.T) {
	pin := strings.TrimSpace(docRead(t, "scripts", "UPSTREAM_SHA"))
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(pin) {
		t.Fatalf("scripts/UPSTREAM_SHA holds %q, not a 40-digit hash: fix the file", pin)
	}
	for _, s := range docPinSentences {
		m := s.re.FindStringSubmatch(docFlat(docRead(t, docRel(s.file))))
		if m == nil {
			t.Errorf("%s no longer holds the sentence that matches %s: reword it back, or update docPinSentences in docs_consistency_test.go", s.file, s.re)
			continue
		}
		if len(m[1]) < 8 || !strings.HasPrefix(pin, m[1]) {
			t.Errorf("%s names the pinned build %s, but scripts/UPSTREAM_SHA holds %s: change the hash to %s", s.file, m[1], pin, pin[:8])
		}
	}
}

// TestDocsCapabilityFeatures: the features array in the server.capabilities example
// of the server page under docs/protocol equals capabilityFeatures, name for name and in order. The
// document shows the Linux list. On Windows osCapabilityFeatures is only files.realpath.how_far, so the
// test takes that name out and puts files.mkdir.verified_parent, git.worktree.external_root, process.spawn.link_bound,
// git.worktree_create.own_lfs_store, git.worktree_create.lfs_error_codes, process.spawn.cli_temp_root and process.spawn.cli_temp_root_end back where methods_server.go appends them (after the
// fixed names, before server.instance_id). Off Linux it puts the two launcher names of
// launcherOSFeatures back after them.
func TestDocsCapabilityFeatures(t *testing.T) {
	const ext = "git.worktree.external_root"
	m := regexp.MustCompile(`"features":\s*\[([^\]]*)\]`).FindStringSubmatch(docRead(t, "docs", "protocol", "server.md"))
	if m == nil {
		t.Fatal("docs/protocol/server.md has no \"features\":[…] array: restore the server.capabilities example, or update docs_consistency_test.go")
	}
	var doc []string
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		doc = append(doc, q[1])
	}
	// launcher.exit_ends_group is the last name on Linux and macOS, and the page shows it.
	want := slices.DeleteFunc(slices.Clone(capabilityFeatures), func(f string) bool { return f == launcherExitFeature })
	hasExt := false
	for _, f := range want {
		hasExt = hasExt || f == ext
	}
	if !hasExt {
		last := len(want) - 1
		fixed := last - len(osCapabilityFeatures)
		want = append(want[:fixed:fixed], "files.mkdir.verified_parent", ext, "process.spawn.link_bound", "git.worktree_create.own_lfs_store", "git.worktree_create.lfs_error_codes", "process.spawn.cli_temp_root", "process.spawn.cli_temp_root_end", want[last])
	}
	if len(launcherOSFeatures) == 0 {
		last := len(want) - 1
		want = append(want[:last:last], "launcher.checked_by_handle", "launcher.local_kinds_only", want[last])
	}
	want = append(want, launcherExitFeature)
	if strings.Join(doc, " ") != strings.Join(want, " ") {
		t.Errorf("the features array of docs/protocol/server.md differs from capabilityFeatures in methods_server.go: make the array in the server.capabilities example read\n%s\nit reads\n%s", strings.Join(want, ", "), strings.Join(doc, ", "))
	}
}

var (
	docErrorCodeRE    = regexp.MustCompile(`ErrorCode:\s+"([a-z_]+)"`)
	docCodeReturnRE   = regexp.MustCompile(`(?m)^\s*return .*,\s*"([a-z_]+)"$|^\s+[^/\n]*\),\s*"([a-z_]+)"$`)
	docCodeResultSigs = regexp.MustCompile(`\bcode string\)`)
	// A span may give the code as errorCode:"x", as the error catalog does.
	docErrorCodeSpanRE = regexp.MustCompile(`errorCode"?\s*:\s*"([a-z_]+)"`)
)

// sendableErrorCodes returns each errorCode that the non-test files can put in a
// response, with the file that names it first. It sees two forms: the literal in
// "ErrorCode: "x"", and the last string of a return line in a file whose functions
// return a "code string" result (internal/git/worktreeexternal_unix.go), which feeds the field.
func sendableErrorCodes(t *testing.T) map[string]string {
	t.Helper()
	files := goSourceFiles(t)
	codes := map[string]string{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		text := docRead(t, file)
		for _, m := range docErrorCodeRE.FindAllStringSubmatch(text, -1) {
			if _, ok := codes[m[1]]; !ok {
				codes[m[1]] = file
			}
		}
		if docCodeResultSigs.MatchString(text) {
			for _, m := range docCodeReturnRE.FindAllStringSubmatch(text, -1) {
				c := m[1] + m[2]
				if _, ok := codes[c]; !ok {
					codes[c] = file
				}
			}
		}
	}
	return codes
}

// TestDocsEveryErrorCodeIsDocumented: each errorCode that the code can send is a
// code span in docs/PROTOCOL.md or in a page of docs/protocol/. In the other
// direction, a table with the header "| `errorCode` | Meaning |" on a method page
// lists only codes that the code can send.
func TestDocsEveryErrorCodeIsDocumented(t *testing.T) {
	codes := sendableErrorCodes(t)
	if len(codes) == 0 {
		t.Fatal("found no errorCode in the non-test .go files: the way the code sets the field changed, update docs_consistency_test.go")
	}
	pages, err := filepath.Glob(filepath.Join("docs", "protocol", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	texts := []string{docRead(t, "docs", "PROTOCOL.md")}
	for _, p := range pages {
		texts = append(texts, docRead(t, p))
	}
	for _, text := range texts {
		for _, sp := range docSpans(text) {
			named[strings.Trim(sp, `"`)] = true
			for _, m := range docErrorCodeSpanRE.FindAllStringSubmatch(sp, -1) {
				named[m[1]] = true
			}
		}
	}
	for c, file := range codes {
		if !named[c] {
			t.Errorf("the code can send errorCode %q (%s), but no document names it: add it to the error table of its method page under docs/protocol/, or to the error catalog of docs/protocol/errors.md", c, file)
		}
	}
	for i, p := range pages {
		inTable := false
		for _, line := range strings.Split(texts[i+1], "\n") {
			switch {
			case strings.HasPrefix(line, "| `errorCode` | Meaning |"):
				inTable = true
			case inTable && strings.HasPrefix(line, "|---"), inTable && strings.HasPrefix(line, "| ---"):
			case inTable && strings.HasPrefix(line, "|"):
				cells := strings.Split(line, "|")
				code := strings.Trim(strings.TrimSpace(cells[1]), "`")
				if _, ok := codes[code]; !ok {
					t.Errorf("%s lists errorCode %q, which the code cannot send: delete the row, or fix the code", filepath.ToSlash(p), code)
				}
			default:
				inTable = false
			}
		}
	}
}

// docWordLimits holds the most words that each document can have. The limits exist
// because the documents grew past what a person can read. A limit goes down, never
// up, without the maintainer's word.
var docWordLimits = map[string]int{
	"AGENTS.md":                                  4738, // 4738: the race detail of the Windows pin moved to the method pages, one fact one home. 5044: the Windows junction rules of baseRepo, by the decisions of 2026-11-23. 4506: the repeat of the deadline kill scoped to a helper that runs at the kill, a claims fix of TASK-889, exact count. 4498: the shutdown drain kill scoped to a leader that stayed in its group and to no -keep-children, and the pty clause scoped to this path, claims fixes of TASK-888 merged with the deadline kill of TASK-875, exact count. 4432: the shutdown drain kill scoped to a leader that stayed in its group and to no -keep-children, a claims fix of TASK-888, exact count. 4485: the deadline kill of TASK-875 merged with the shutdown group of TASK-877, exact count. 4398: the one repeat of the deadline kill marked claustrum's own, a review fix of TASK-875, exact count. 4394: the stdin sentence left for its one home on the method page, a review fix of TASK-875, exact count. 4415: the stdin clause scoped to a helper that runs at the kill, a review fix of TASK-875, exact count. 4412: the stdin clause scoped to the three long-running helpers, a review fix of TASK-875, exact count. 4407: the file hash gets no repeat, 87375cc7, TASK-875 under the maintainer decision of 2026-11-23, exact count. 4402: the deadline kill of the four helpers on three systems, 87375cc7, TASK-875 under the maintainer decision of 2026-11-23, exact count. 4403: the Part A line of the deadline kill of the content check, TASK-875 under the maintainer decision of 2026-11-23, merged with main, exact count. 4422: the macOS rows, the pty exclusion and the second kill after a drain KILL, 87375cc7, TASK-877, exact count. 4416: the reuse and outside-holder rows of 87375cc7 under the maintainer decision of 2026-11-23, TASK-877, exact count. 4384: the group SIGKILL of a plain child in its exit drain at a shutdown on Linux, 87375cc7 and 4c111e86, TASK-877, exact count. 4335: no SIGKILL for a refused SIGTERM, TASK-872, exact count. 4328: the limits of the macOS kill and the refusal stated, TASK-765, exact count. 4301: the group SIGKILL of a shutdown in the wait of an exited launcher, TASK-765, exact count. 4279: the group of an exited launcher, 87375cc7, TASK-765, exact count. 4197: the claims fixes of the unattended end, 87375cc7, TASK-853 under the decide-alone rule of 2026-11-20, exact count. 4206: the unattended end of TASK-853 on top of the 4118 of main (its Part A line, the Linux rows, the launcher rule and the review fixes), 87375cc7, under the decide-alone rule of 2026-11-20, exact count. 4142 and 4119 on the branch before the merge. 4118: the own table row of gitshellpath.go and a SIGKILL with no count, TASK-773 claims fixes under the decide-alone rule of 2026-11-20, exact count. 4101: the Part A line of the --exec-path stop of the login git and gitshellpath.go in the file table, TASK-773 under the maintainer decision of 2026-11-21, exact count. 4054: the scratch folder goes when the check ends, a review nit of TASK-825 under the decide-alone rule of 2026-11-20, exact count. 4051: the claustrum-own mark of the scratch folder clause on the folder time only, a review fix of TASK-825 under the decide-alone rule of 2026-11-20, exact count. 4057: the scratch folder clause of the content check marked claustrum's own and scoped to the cat-file --batch, a claims fix of TASK-825 under the decide-alone rule of 2026-11-20, exact count. 4052: the time of the remove of the scratch folder of the content check at a timeoutMs deadline, 4c111e86, TASK-825 (the maintainer asked for a Part A line, 2026-11-21), under the decide-alone rule of 2026-11-20, exact count. 4039: the startup reap row and bullet of TASK-711 (the maintainer's go for about 70 words, 2026-11-20) merged with main, exact count. 3960: with the window clause of the remove, a review fix. 3944: the scratch file remove of the content check, a new git-shadow delete site of 4c111e86 (maintainer: follow, 2026-11-20), its exception word, its file in the table and two headings that keep the Part A section under its run limit, TASK-730, exact count. 3865: the other errors of the resume recorded and two words of main restored, a review fix, TASK-716 under the decide-alone rule of 2026-11-20, exact count. 3863: the restored build and the names awaitJobTest and beginJobTest of the job start test line, a review fix, exact count, raised by the maintainer's decide-alone rule of 2026-11-20 for TASK-716. 3860: the end at an asleep failure of TASK-716 fits in the same count. The job start test line of TASK-694 after TASK-680 merged, the maintainer's exception of 2026-11-18 (exact count after both), do not raise again. 3821: the maintainer's exception of 2026-11-18 for the Path 3 Never widen lines of the branch step behind a link and of the folder in the place of the leaf, and the held-leaf write of the Writes rule (TASK-680). TASK-694 raises it once more under the same exception. Do not raise again. 3774: the pin of 4c111e86, one build name more, exact count (decision-062). 3773: the identity home guard of the worktree leaf deletes, TASK-044 (maintainer decision 2026-11-16), exact count. 3771: the group kill after a failed read-tree call, 4c111e86, exact count (decision-062). 3696: the loss kinds of a private temp folder, merged with main, exact count (decision-062). 3648: no overrun kill after a wait with no exit status, exact count (decision-062). 3636: the overrun kill of the read-tree call before the reap of git, exact count (decision-062). 3638: the signal line of a give-up during a start, 4c111e86, with its Windows end, exact count (decision-062). 3584: the bound stops of -install and -probe-cli send their group kill only before the reap, and the gate is sysproc.LeaderGate, exact count (decision-062). 3559: the merge of the timeoutMs stop bullet of PR 1104 with the request-folder lines of main, exact merged count (decision-062). 3521: the mkdir identity in the request-folder Never widen line, a review fix (decision-062). 3507: an empty request folder and the folder of the daemon in the Never widen line, claim fixes, with the shard row of main, exact count (decision-062). 3504: the same before the shard row. 3501: the parent identity of the rmdir at mode 0300 and its macOS window, a review fix, exact count (decision-062). 3461: the rmdir of an empty request folder at mode 0300, 4c111e86, exact count (decision-062). 3400: the three internal/git shards of make test in the Tests row, exact count (decision-062). 3397: the rename rule of the keep swap and the second open of the keep copy, claim fixes, exact count (decision-062). 3384: the macOS label of the keep swap, a claim fix, exact count (decision-062). 3381: the keep swap of the LFS store, 4c111e86, exact count (decision-062). 3354: the macOS stop, the read-tree group, the no-signal-after-reap rule, the mode 0300 folder and the stop of the keep, claim fixes, exact count (decision-062). 3346: the threshold and systems of the LFS space stop, a claim fix, exact count (decision-062). 3339: the LFS space stop bullet, 4c111e86, exact count (decision-062). 3274: the .probe of the folder of the daemon, a claim fix, exact count (decision-062). 3268: the git-shadow bullet names the root at `.probe`, a claims fix, exact count (decision-062). 3266: the probe deletes through a root at `.probe` (removeProbeEntry), 4c111e86 mode 0300, exact count (decision-062). 3267: the Steps that move the hold heading and askedAgain in its list, exact count (decision-062). +2 for the home guard of the HEAD copy rmdir (decision-062). 3288: the third step that moves the hold, 4c111e86, exact count (decision-062). 3266: the HEAD copy rmdir file in the table and as an exception of the git-shadow bullet, exact count (decision-062). 3259: the seed rule of copySeed back word for word as its own rule, exact count (decision-062). 3237: the empty HEAD folder of a repository copy, 4c111e86, and a shorter seed line in the stack merge, exact count (decision-062). 3234: the seed copy remove as an exception of the git-shadow bullet, exact count (decision-062). 3228: the remove of the object file of a failed seed copy, 4c111e86, exact count (decision-062). 3195: a file or link in place of the leaf of a failed add, c9125eb8 and 4c111e86 (decision-062). 3183: the temp-folder sites on macOS, exact count (decision-062). 3179: the signal sites of a lost temp folder, 4c111e86, exact count (decision-062, decision-099). 3112: the inner t of the new temp folder, its rmdir and its heading, 4c111e86, exact count (decision-062). 3085: the LFS store sweep of 4c111e86, a new delete site, exact count (decision-062). 3028: the second method that Windows lacks, files.find (decision-062). 3027: the temp folder delete of a request, exact count (decision-062). 3029: pointer index, TASK-451 Branch side, 3438: the merge of the timeoutMs stop bullet with the three internal/git shards in the Tests row, exact merged count (decision-062). 3435: the timeoutMs stop of the read-tree call as its own bullet that names githarden.go, a claims fix, exact count (decision-062). 3432: cmd.Cancel named in the deadline stop rule and the drain overrun kill out of its scope, a review fix, exact count (decision-062). 3417: the deadline stop of the read-tree call sends its group kill only before the reap, exact count (decision-062).
	"CONTRIBUTING.md":                            1071,
	"README.md":                                  1124, // the same-methods sentence cut, exact count. 1133: 13 are the HTML tokens of the logo <picture> block (maintainer, 2026-10-31)
	"SECURITY.md":                                1617, // the adopt frame takes auth from daemon.token, claim fix (decision-080). 1609: the ssh door of be1c1ca5. 1553 before
	"docs/ARCHITECTURE.md":                       2669, // the bridge, clitemproot, sessionfolders, shutdownreply and spawnsteps files in the file map, with a cut, exact count (TASK-708). 2671: pluginhostsibling_*.go out of the file map again, exact count. 2672: pluginhostsibling_*.go in the file map (decision-080). 2671: the same-methods sentence cut, exact count. 2682: gitsshdoor.go. 2674: socketroute*.go. 2673: the sweep on Linux and macOS of be1c1ca5. 2667 before
	"docs/DAEMON.md":                             9206, // 9206: up to about 40 ms and in most runs for the macOS realpath line, TASK-905, exact count. 9201: the refused-kill shutdown wait on Linux, its measured and own cases, TASK-898, exact count. 9141: the macOS wait of the realpath start line, with its accept delay, its held line and its warm-off case (own), TASK-886, exact count. 9104: the macOS place of the start line after the host cleaning off line and its order with a fast login shell, TASK-876, exact count. 9078: the macOS start line of files.realpath, 4c111e86 and 87375cc7, its order by timing, TASK-882, exact count. 9037: the WARN line of a failed removal of CLAUDE_SSH_LEAVE_GROUPS, TASK-765, exact count. 9005: the group of an exited launcher, 87375cc7, TASK-765, exact count. 8985: the claims fixes of the unattended end, 87375cc7, TASK-853 under the decide-alone rule of 2026-11-20, exact count. 8970: the macOS mark of the exit 2 with no HOME, a review nit of TASK-827 under the decide-alone rule of 2026-11-20, exact count. 8966: the exit 2 with no HOME moved after the re-exec sentence, with the Windows USERPROFILE as from the code, a claim fix of TASK-827, exact count. 8941: the exit 2 of -serve with no home folder, 4c111e86, TASK-827, exact count. 8910: the spawn scope of the slow temp folder line and the folder exception of the swap rule, review fixes of TASK-711 and TASK-736, exact count. 8894: the swap rule and the two risks of the startup reap (TASK-711) and six words cut from the slow temp folder line (TASK-736), exact count. 8823: the claustrum-own level of the survivor line, a review fix, TASK-717 under the decide-alone rule of 2026-11-20, exact count. 8818: the 4c111e86 text and evidence tag of the line of a group that outlives the reap SIGKILL, and the claustrum-own mark of its kept record of a review fix, the exact count of the fix's own text (TASK-717, the decide-alone exception of 2026-11-20). 8803: the line of a group that outlives the reap SIGKILL, claustrum's own, with cuts on the page, exact count (TASK-707). 8803: the `host cleaning off` line and c9125eb8 in the held order of the realpath start line, a review fix of three words, exact count (decision-062). 8800: the place of the realpath start line on Linux and Windows, 4c111e86, three new rule sentences of 44 words, exact count (decision-062). 8756: the serving line after a failed own naming and the end of the reads, 4c111e86, measured on Linux, exact count (decision-062). 8726: the peer check start line of 4c111e86, measured on macOS and Windows, exact count (decision-062). 8714: the steady pause after a failed accept, 4c111e86 and c9125eb8, exact count (decision-062). 8680: the Windows exit line at shutdown, exact count (decision-062). 8667: the cut id of the unattended and lost temp folder lines, exact count (decision-062). 8639: no exit line for a child that the shutdown ends, measured on Linux, and the macOS counts, exact count with review fixes (decision-062). 8618: no exit line for a child that the shutdown ends, c9125eb8 and 4c111e86, exact count (decision-062). 8595: the net-only start line and the refusing line of the peer check, 4c111e86 (decision-062). 8527: a long sentence of the id cut split in two, a review fix (decision-080). 8525: the cut of a long id in four process lines of c9125eb8 and 4c111e86 (decision-062). 8455: the merge of the child-record claim fixes (8412) with the stamped-orphan claim fixes of PR 1028 (8383), exact merged count (decision-080). 8412: the measured link and no-record cases of the child record remove, claim fix of a review (decision-080). 8374: the start-only remove of the child record and the cannot-read reap line of c9125eb8 (decision-080). 8340: the supplies start line of c9125eb8 (decision-080). 8317: the remove rules of the child record, claim fix of a review (decision-080). 8225: claim fixes of the stamped orphans and the 3 s wait, c9125eb8 (decision-080). 8207: the long-root approval and the daemon.lock parity, moved from AGENTS.md (TASK-451). 8187: one lsof run after a run that it gave up on, b3081b51. 8180: the refused signal line. 8150: the cleanup word of b3081b51. 8137: the signals at a long root, measured. 8115: the not-measured parts of a long run root. 8090: the long run root of the host cleaner. 7940: the long socket path. 7616: the start-time line. 7541: the temp-folder lines of 683cc6a9. 7493: the new section Lines of connections. 7175: a new page, its words moved from PROTOCOL.md and ARCHITECTURE.md
	"docs/DIVERGENCES.md":                        7987, // 7987: the 1.5 s kill also after a refused SIGTERM, and the pty rule scoped to an exited leader, a claims fix of TASK-888, exact count. 8299: D28 scopes the drain kill to a leader that stayed in its group and the 1.5 s kill to a child that got the SIGTERM, a claims fix of TASK-888, exact count. 8287: the claims fixes of the shutdown drain kill, TASK-877, exact count. 8269: the macOS rows, the pty exclusion and the second kill after a drain KILL, 87375cc7, TASK-877, exact count. 8279: the reuse and outside-holder rows in D28 of 87375cc7 under the maintainer decision of 2026-11-23, TASK-877, exact count. 8254: the SIGKILL of a member on the pipes right after the reap in D28 on Linux, 87375cc7 and 4c111e86, TASK-877, exact count. 8237: the 87375cc7 measurement of D28 for a child with no launcher on Linux and macOS, a review fix of TASK-765 under the decide-alone rule of 2026-11-20, exact count. 8220: D28 scoped to a launcher spawn with the group end on, TASK-765, exact count. the deadline sentence of the opt-in bounds, moved from AGENTS.md (TASK-451). 8199: the lock that counts in the D22 GIT_DIR state. 8193: the missing daemon GIT_DIR state of D22, its gitdir record and the named locked entry, b3081b51. 8152: the Without worktreeRoot scope of the D2 junction test. 8153: the home guard of the handle path of a Windows remove. 8143: the Windows clause of the missing baseRepo cut. 8149: the cut of the held form of D27. 8166: the pin of b3081b51: the measured D27 reply. 8200: D28. 7908: the staged add of be1c1ca5. 7778: the cap that skips the prune after a full disk. 7764: the summary table
	"docs/EXAMPLES.md":                           1025,
	"docs/GLOSSARY.md":                           1297,
	"docs/IMPROVEMENTS.md":                       1611, // 1611: the claims fixes of the shutdown drain kill, TASK-877, exact count. the KILL exception of item 21, b3081b51
	"docs/INSTALL.md":                            5761, // 5761: the -install scope of the exit 1 with no home folder and its -serve pointer, a review fix of TASK-827 under the decide-alone rule of 2026-11-20, exact count. 5756: the parity mark of the single-segment row, a claims fix of TASK-675, exact count. 5755: the zstd window refusal of 4c111e86 and the systems of its window row, TASK-675, exact count. 5725: the Windows cases of the final progress line, duplicate texts cut, a review fix, exact count. 5727: the outlive claim of the probe cut and the group kill of the launcher stop only while the run runs, exact count. 5733: the blob remove with no guard, moved from AGENTS.md (TASK-451). 5727: the download texts and progress lines of b3081b51, the no-length row and the scope of the final line rule and the two values of the final-chunk wait. 5712: before them. 5659: first count. 5585: the measured no-fallback rule for an ldd that cannot run. 5573: the measured /bin/ldd fallback of b3081b51. 5568: the not-measured /bin/ldd mark of b3081b51. 5560: the fixed ldd path and environment of b3081b51. 5539: the blob guard of the new prunes. 5531: the measured rows of the prune. 5508: the prune before the fetch of -install. 5307: page rewrite
	"docs/PROTOCOL.md":                           2904, // the files.find row, exact count. 2890: the claim fix of the -32008 row, its link to the state fallback-git (decision-080). 2886: the -32008 row of gitRead in fallback-git, c9125eb8 (decision-080). 2863: the merge of the server.adoptEnv row with the files.mkdir row, exact count. 2855: the parent of files.mkdir in the tilde rule, claim fix of a review (decision-080). 2847: the merge of the files.mkdir row with the launcher rows of main. 2833: the files.mkdir row, exact count. 2835: the files.realpath row and count. 2826: one sentence on the close line. one sentence on a failed write. the error texts, stream frames and flags moved to their own pages
	"docs/STYLE.md":                              1291, // the exception of REFERENCE-BUILDS.md cut, exact count (TASK-673). 1293: the page rewrite
	"docs/TOUR.md":                               850,  // a new page: its word count plus about 200
	"docs/UPSTREAM-TRACKING.md":                  6286, // 6286: a residual of the junction trust, TASK-894, exact count. 6250: the Windows junction note cut to its claims. 6322: the refusal of a keyed pty on Windows, 4c111e86 and 87375cc7, TASK-870, exact count. 6336: D28 scoped to the exit frame and a launcher spawn, TASK-765, exact count. 6318: the static check items said as the script runs them, review fixes of TASK-816, exact count. 6299: the gone .git file item of git.info cut, out of date, a claims fix of TASK-635, exact count. 6342: Linux named beside macOS in that listing item, a review fix of TASK-635, exact count. 6340: the macOS scope of the config listing after the start test on a .git file to a missing path, a claims fix of TASK-635 under the decide-alone rule of 2026-11-20, exact count. 6336: the git.status junction answer cut, 4c111e86, exact count. 6358: the build of the start test of the jobs, a review fix, exact count. 6359: the pin of 4c111e86: the launcher and files.find items name the pin, and the builds of supplies_cli and the ssh door entries, a claims fix, exact count (decision-062). 6359: the seed calls of 4c111e86, exact count (decision-062). 6360: the stale bullet of the shutdown exit line cut, a review fix (decision-062). 6415: the out_of_resources line of the peer check, 4c111e86, a review fix (decision-062). 6410: the private LFS store of 4c111e86 and its seed calls (decision-062). 6370: the files.find line of 4c111e86 (decision-062). 6354: the .checkout-<n> folder and the lfs.storage item of 4c111e86 (decision-062). 6333: the macos_host text of 4c111e86, a triage claim fix. 6330: the triage claim fix of the launcher texts of 4c111e86 (decision-090). 6323: the pin of c9125eb8: the launcher sentence tagged as the pin, the build of cliRemoved, the run-dir test of a stamped orphan. 6342: the launcher sentence of c9125eb8 (decision-080). 6333: the stamped orphan and 3 s wait item, c9125eb8 (decision-080). 6291: the missing daemon GIT_DIR in the D22 row. 6284: the junction trust-root item cut, b3081b51. 6313: the download texts of b3081b51. 6386: the pin of b3081b51: the content check item cut, two pin words added. 6411: the network link exception of b3081b51. 6401: the claims fixes of m-19. 6404: the content check of b3081b51 and its commit-id call. 6335: the junction item of b3081b51. 6312: the D28 row. 6285: the pin of be1c1ca5: six triage items cut, three pin sentences added. 6480: the staged add of be1c1ca5. 6418: the ssh door of be1c1ca5. 6400: the links of the long-path dials. 6392: the long-path dials of the host cleaner and plugins.prune. 6379: the long socket path. 6356: the sweep on Linux and macOS of be1c1ca5. 6329: the git env bullet. 6300: the worktree texts of be1c1ca5. 6250: the build of the new prunes. 6234: the pin of 683cc6a9. 6232: the terminal of 683cc6a9. 6230: the build of the open destDir refusal. 6228: the could-not-be-examined text. 6224: words moved from PROTOCOL.md, net down in the same PR
	"docs/index.md":                              325,  // the Go pin item (decision-076). 316: the reader router
	"docs/protocol/errors.md":                    2048, // 2048: the bad form of each system in the git_not_found mark, a claims fix of TASK-867, exact count. 2045: the measured text of each system in the git_not_found mark of the bad-count row, a claims fix of TASK-867, exact count. 2038: the four no-git answers sent to the scope on their pages, with not_a_repo named, a review fix of TASK-785, exact count. 2038: the systems of the disagree row split by worktreeRoot, a claim fix of a review (decision-080). 2037: the macOS worktreeRoot case of the disagree row, c9125eb8 (decision-089). 2035: the build tag and link of the remove case (review). 2027: the remove case of the disagree row, c9125eb8 (decision-088). 2015: the root as sent and the quoting and cut of the link-limit text. 1978: the link-limit backticks. 1957: the .probe text, 41 words of a new row
	"docs/protocol/files-extract-tar.md":         2463, // the file that Windows cannot read. 2457: the empty folder that Windows cannot read. 2420: the .synced rules of 683cc6a9
	"docs/protocol/files-find.md":                1290, // the two-root order of paths measured on Linux, exact count. 1291: the stuck line of 4c111e86 and the slow walks, exact count (decision-062). 1288: a new page: its word count (1088) plus 200, 4c111e86
	"docs/protocol/files-mkdir.md":               536,  // the no-/proc and exact-compare rules, claim fix of a review (decision-080). 508: the name test, the parent codes, the real-path check, the umask mode and the errno codes of c9125eb8, exact count (decision-080). 402: the failed line and the errno 36 scope, claim fix of a review (decision-080). 380: claim fixes of the files.mkdir page, exact count (decision-080). 538: a new page: its word count plus about 200, c9125eb8
	"docs/protocol/files-list.md":                550,  // the ELOOP tries of b3081b51. 468: the network links of 683cc6a9
	"docs/protocol/files-read.md":                825,
	"docs/protocol/files-realpath.md":            1188,  // 1188: the Windows atLetter and stoppedShort members of 87375cc7 and the 1921 of a loop of TASK-778 on top of the 1027 of main, under the decide-alone rule of 2026-11-20, exact count. 1027: the trailing-slash section of 4c111e86 with its measured frames on top of the 974 of main, TASK-835 under the decide-alone rule of 2026-11-20, exact count. 974: the missing folder between and the walk by text named, a review fix of TASK-806 under the decide-alone rule of 2026-11-20, exact count. 962: the link count over more than one name, a rootless link target and another failure of the nearest folder marked as claustrum's own, a review fix of TASK-806 under the decide-alone rule of 2026-11-20, exact count. 915: the limit and the errno of the moved own cases spelled out, a review nit of TASK-806 under the decide-alone rule of 2026-11-20, exact count. 913: the colon-test order and the two SUBST cases marked as claustrum's own, with a heading of their own, a review fix of TASK-806 under the decide-alone rule of 2026-11-20, exact count. 878: the measured and own cases of a missing folder on Windows split into two sentences, a review nit of TASK-806 under the decide-alone rule of 2026-11-20, exact count. 873: the Windows failure of the nearest existing folder for a path below a missing folder, with its own heading, a review fix of TASK-806 under the decide-alone rule of 2026-11-20, exact count. 825: the measured colon paths in the row, a review nit of TASK-806, under the decide-alone rule of 2026-11-20, exact count. 821: the measured scope of the colon, network-link and UNC rows, review fixes of TASK-806 under the decide-alone rule of 2026-11-20, exact count. 787: the Windows SUBST, mapped-drive, junction-chain, missing-name and stream rows of 4c111e86, TASK-806, TASK-807 and TASK-822 under the decide-alone rule of 2026-11-20, exact count. 711: the <file>/ row named as a gap, not a divergence, a local review nit of TASK-784, exact count. 695: the Windows forms with . and .., the <file>/ row and the macOS scope of missingLeaf, review fixes of TASK-784 under the decide-alone rule of 2026-11-20, exact count. 631: the measured scope of a path below a regular file, a review fix of TASK-784, exact count. 619: a path below a regular file, 4c111e86, TASK-784 under the decide-alone rule of 2026-11-20, exact count. 587: its own row, exact count (TASK-673, maintainer decision 2026-11-17)
	"docs/protocol/line-end.md":                  239,   // its own row, exact count (TASK-673, maintainer decision 2026-11-17)
	"docs/protocol/process-farewell.md":          526,   // its own row, exact count (TASK-673, maintainer decision 2026-11-17)
	"docs/protocol/files-stat.md":                514,   // the ELOOP tries of b3081b51. 434: the page rewrite
	"docs/protocol/files-validate.md":            445,   // the ELOOP tries of b3081b51
	"docs/protocol/flags.md":                     2737,  // the shutdown pointer to the stop rules, a review fix, exact count. 2738: the token pipe before the markers, a review fix (decision-062). 2730: the daemon environment drops the launcher marks, c9125eb8 and 4c111e86, exact count with a review fix (decision-062). 2653: the =1 gate and the relayed-line auth, claim fix (decision-080). 2649: the pointer to the adoptEnv frame of the bridge, c9125eb8 (decision-080). 2639: the prune after a good install. 2633: moved from PROTOCOL.md
	"docs/protocol/git-info.md":                  2148,  // 2148: the Windows junction rules. 2152: no git on the login shell's PATH either, linked, TASK-773 claims fixes under the decide-alone rule of 2026-11-20, exact count. 2135: the pointer to the GIT_ALTERNATE_OBJECT_DIRECTORIES refusal, TASK-857 under the decide-alone rule of 2026-11-20, on top of the 2130 of main, exact count. 2130: such a request in place of it before a kept state, a review fix of TASK-846 under the decide-alone rule of 2026-11-20, exact count. 2128: the known gap of the four calls replaced by a pointer to the per-call rule, TASK-846 under the decide-alone rule of 2026-11-20, exact count. 2143: the unkept start test of TASK-836, its retest, its Linux mark and the measured first request on macOS and Windows on top of the 2072 of main, under the decide-alone rule of 2026-11-20, exact count. 2072: the known gap of the first request with sessionFoldersOff, a review fix of TASK-786, under the decide-alone rule of 2026-11-20, exact count. the Windows rows of the git 2.56 gitfile answers, TASK-729, exact count. 2042: the macOS rows of the git 2.56 gitfile answers, TASK-729, exact count. 2043: the gone git dir of a .git file with git 2.56 on Linux and macOS, 4c111e86, TASK-546 under the decide-alone rule of 2026-11-20, exact count. 1998: the \\?\UNC\ and share refusals of b3081b51. 1983: the network link test of b3081b51. 1947: the link chain row of its own (Linux and macOS run no link test). 1889: the link chain of git.info, b3081b51. 1881: the alternates row of b3081b51. 1850: the page rewrite
	"docs/protocol/git-rules-calls.md":           6523,  // 6523: the Windows reply that does not wait for a holder of stdout after the timeoutMs stop of a read-tree call, 87375cc7, TASK-890 under the decide-alone rule of 2026-11-20, exact count. 6467: an answer with no login-shell git held 10 s, a review fix of TASK-773 under the decide-alone rule of 2026-11-20, exact count. 6445: the claustrum-own rules of the login-shell git in one list, with the kept session-folder answer, a review fix of TASK-773 under the decide-alone rule of 2026-11-20, exact count. 6409: the walk over each git of the login shell's PATH, git.status and git.worktree_remove as claustrum's own, the check-on scope, TASK-773 claims fixes under the decide-alone rule of 2026-11-20, exact count. 6375: no login-shell read started by a git call and the stdout wait of the --exec-path call, review fixes of TASK-773 under the decide-alone rule of 2026-11-20, exact count. 6350: the one-process SIGKILL of the --exec-path call and the session-folder git, TASK-773 under the decide-alone rule of 2026-11-20, exact count. 6325: the git of the login shell's PATH of 87375cc7, TASK-773, merged with main under the decide-alone rule of 2026-11-20, exact count. 6112: the two-slash values of 4c111e86 on Windows of TASK-858 as one absolute rule, the LD piece exception and a heading that splits the variables section under its run limit, under the decide-alone rule of 2026-11-20, on top of the 6068 of main, exact count. 6068: the pointer of the excludes read to the rule before a kept state, a review fix of TASK-846 under the decide-alone rule of 2026-11-20, exact count. 6064: the five names of the Windows excludes read of TASK-837, its git.status scope and the other-method mark, on top of the 6023 of main, under the decide-alone rule of 2026-11-20, exact count. 6023: the UNC forms of 4c111e86 on Windows of TASK-834 on top of the 6015 of main, under the decide-alone rule of 2026-11-20, exact count. 6015: the after-the-add call scoped to a checkout of a branch, and the add of a commit id marked as claustrum's own, a review fix of TASK-832 under the decide-alone rule of 2026-11-20, exact count. 5987: the claustrum's own mark of the attach listing with no git.info first, a review fix of TASK-832 under the decide-alone rule of 2026-11-20, exact count. 5975: the macOS and after-a-git.info scopes and the Linux and Windows marks of the TASK-832 sentences, a review fix of TASK-832 under the decide-alone rule of 2026-11-20, exact count. 5953: the macOS call about the repository after a hold that ends at the add and the extra listing of an attach add, 4c111e86, TASK-832 under the decide-alone rule of 2026-11-20, exact count. 5879: TASK-808 merged with main, exact count. 5841: the git.info scope of the measured version run, and macOS and Windows marked as claustrum's own, a review fix of TASK-828, under the decide-alone rule of 2026-11-20, exact count. 5816: the measured exceptions of the outside calls, a review fix of TASK-828, under the decide-alone rule of 2026-11-20, exact count. 5813: the LC_ALL and LANGUAGE of the version run and the env of the version run of the probe, 4c111e86, TASK-828, exact count. 5790: the UNC mark reverted, a claims fix, exact count. 5793: the from-the-code mark of the UNC rule of the git env filter, exact count (TASK-709). 5795: the group kill of a failed checkout on Linux, 4c111e86, exact count (decision-062). 5781: the place of the trace entries of the start test, 4c111e86, exact count (decision-062). 5778: the held create and the record-mismatch pair as claustrum's own, a claim fix of a review (decision-080). 5732: the two pairs of a create with the member outside fallback-git get false, c9125eb8 and b3081b51. 5720: the settle pair in the limit rule, a claim fix of a review (decision-080). 5715: the Windows sentence of the missing baseRepo cut. 5724: the count of a missing baseRepo below a marker. 5713: the colon form of the count and the start test of a gone remove, b3081b51. 5660: the failed read-tree call of b3081b51 with a child on both pipes past the deadline, Linux and macOS. 5625: the failed read-tree call of b3081b51 with a child on stdout, the drain and a short stderr hold. 5535: the ssh door entries of be1c1ca5. 5276: the trace and ssh entries. 5145: the core.hooksPath value. 5121: moved from PROTOCOL.md
	"docs/protocol/git-rules-config.md":          3244,  // 3244: a residual of the junction trust, TASK-894, exact count. 3246: the includeIf conditions as measured and the others as from the code, and the HOME of a ~ include as measured, claim fixes of TASK-827 under the decide-alone rule of 2026-11-20, exact count. 3247: the ~/ include kept as written and the refused includeIf file with a bad line, 4c111e86, TASK-827 under the decide-alone rule of 2026-11-20, exact count. 3214: every other shallow link as claustrum's own. 3187: the shallow link of b3081b51 on Windows. 3128: the config link to a pipe of b3081b51, and the includes as claustrum's own. 3082: the junction include of b3081b51, its methods, the 0x9000001A follow and the walk as claustrum's own. 2981: the other colons and reserved names as claustrum's own. 2943: the NUL and stream includes of b3081b51. 2929: the listing of b3081b51 over 64 MiB and the read while git writes it. 2884: moved from PROTOCOL.md
	"docs/protocol/git-rules-settings-copy.md":   4372,  // 4372: the Windows calls of a request with a daemon GIT_DIR, 87375cc7, TASK-902, exact count. 4285: the Windows content check asks again after 5 s (own), TASK-880; 4262: the change half of the state line credited to 5fd08069, and the kept shadow row, a review fix of TASK-836 under the decide-alone rule of 2026-11-20, exact count. 4254: the state line scoped to a test that gives a state, and the failed test of 5fd08069, a review fix of TASK-836 under the decide-alone rule of 2026-11-20, exact count. 4243: the measured mark of a failed test under sessionFoldersOff kept, and the build and system marks of the state line, a review fix of TASK-836 under the decide-alone rule of 2026-11-20, exact count. 4209: git.list_branches beside git.info with no git on PATH, a review fix of TASK-785, exact count, at the 4209 that is not raised again. 4207: the summary sentence that lists the sections cut, a review fix of TASK-730, exact count, below the 4209 that is not raised again. 4216: the .readback-scratch-<n> folder of 4c111e86 and its emptied rmdir at mode 0300, TASK-730, merged with main after PRs 1252 and 1255, exact count. 4203: TASK-742 merged with the 4c111e86 create marks of TASK-728, exact count. 4205: the version text cell that also holds an empty stdout, a review nit of TASK-742. 4209: Linux added to the two 4c111e86 create marks, TASK-728 under the decide-alone rule of 2026-11-20, exact count, at the 4209 that is not raised again. 4207: the shadowDirOff index name scoped to the temporary folder, a review fix of TASK-732, exact count, below the 4209 that is not raised again. 4206: a repeated sentence cut and the LFS sentence given its folder and build, review fixes of TASK-732, exact count. 4208: the claustrum-own label of the Windows index folder name, a review fix of TASK-732, exact count, below the 4209 that is not raised again. 4207: the 4c111e86 marks of the two Windows create clauses, a review fix, TASK-635 under the decide-alone rule of 2026-11-20, exact count, below the 4209 that is not raised again. 4199: the Windows create in the no-member row and the missing path named, a claims fix of TASK-635, exact count. 4192: the out-of-date gone .git file sentence cut and the Windows create named, a claims fix of TASK-635, exact count. 4209: the heal scope and the Windows empty folder, a review fix, exact count. 4210: the maintainer's exception of 2026-11-18: +20 for the full bound of the git-shadow folder heal, do not raise again. 4190: the rmdir of an empty request folder at mode 0300, 4c111e86, exact count (decision-062). 4181: the macOS stop, the read-tree group, the no-signal-after-reap rule, the mode 0300 folder and the stop of the keep, claim fixes, exact count (decision-062). 4167: the request folders that stay at mode 0300, 4c111e86, exact count (decision-062). 4151: a daemon folder of mode 0300, 4c111e86, exact count (decision-062). 4106: the end place of GIT_COMMON_DIR in the staged add, 4c111e86, exact count (decision-062). 4093: the rmdir of the empty HEAD folder by system, a review fix, exact count (decision-062). 4080: the empty HEAD folder removed, 4c111e86, exact count (decision-062). 4046: the new copy of a HEAD of mode 000 or no bytes, 4c111e86, exact count (decision-062). 3998: the claim fixes of the checkout folder remove, exact count (decision-062). 3995: the remove of the checkout folder after the reply, 4c111e86 (decision-062). 3973: the claude-ssh-checkout- folder name of 4c111e86 (decision-062). 3974: the git-shadow flock errors that go on, c9125eb8, both name forms (decision-100), and the claustrum-own cases (decision-062). 3856: the .checkout-<n> folder of 4c111e86 and its sweep (decision-062). 3761: the merge of the Linux ask and the 8 calls, exact merged count (decision-080). 3758: the staged row and the claustrum-own parts of the 8 calls, a claim fix of a review (decision-080). 3736: the 8 calls of a remove after a second disagree answer, c9125eb8 (decision-089). 3705: the ask before the content check after the add of a commit, c9125eb8 (decision-089). 3704: the Linux ask after the add, c9125eb8 (decision-089). 3702: the -b add scope of the macOS ask, a claim fix of a review (decision-080). 3696: the sourceBranch scope and anchor of the macOS ask, a claim fix of a review (decision-080). 3689: the macOS ask after a good add of a create, c9125eb8 (decision-088). 3678: the lock-file and privilege scope of the reason alone, and the claustrum-own printed path of Linux, macOS and a relative answer. 3630: the Windows scope of the lock text and the printed path. 3591: the claustrum-own kinds and places of a file that is not a folder. 3536: the error-line join, the measured setup calls of the version text and the FIFO or device file of git.info and git.status of b3081b51, and the regular file of git.status as claustrum's own. 3444: the ended first config --get, and its exception in the killed row. 3439: the ended first config --get of the start test, b3081b51. 3411: the tree of mktree in the start test, b3081b51. 3363: the .readback-objects-<n> entry of b3081b51 and a heading. 3340: the start-test cannot-run rule of b3081b51. 3300: the third run of the call about the repository. the folder .probe, new rules
	"docs/protocol/git-rules-trust.md":           5590,  // 5590: the Windows shadow pin of a daemon GIT_DIR, TASK-902, exact count. 5559: the stat error and the measured methods of that junction, TASK-881, exact count. 5481: the daemon GIT_DIR out of the link-refusal list, a review fix of TASK-861, exact count. 5463: the Windows hold and the daemon GIT_DIR scoped against the stale notes, a review fix of TASK-861, exact count. 5434: a git file through a junction, TASK-861, exact count. 5416: the device link and alternates refusals of a remove, TASK-861, exact count. 5411: the GIT_COMMON_DIR exception marked as claustrum's own, with 4 words cut in the same section, a review fix of TASK-818, exact count. 5411: TASK-729 merged with main, exact count. the maintainer's exception of 2026-11-18: +24 for the junction note of PR 1203, do not raise again. 5388: the Windows link, share and reparse tag rules moved to git-rules-windows-paths.md, the maintainer's split of 2026-11-18, exact count. 5884: the owner exception and the labels of the search rows, review fixes, exact count (decision-062). 5869: the create refusals of a folder and a .git that the daemon cannot search, 4c111e86, exact count (decision-062). 5811: the commondir value ./. and the link to the unusable-git refusal of 4c111e86 (decision-062). 5782: the stale start sentence cut. 5801: the missing daemon GIT_DIR, the .git file to a regular file and the gone worktree with git 2.56, b3081b51. 5720: the \\?\UNC\ and share refusals of b3081b51. 5521: the M4 OS error reason of b3081b51, and the other OS errors of S2 and M4 as claustrum's own. 5494: the order and the cleaned path of the network link text. 5472: the network link test of b3081b51. 5254: the gone worktree and the link test of git.info, b3081b51. 5207: the object sources on Windows of b3081b51. 4642: the 1 MiB commondir reason of be1c1ca5. 4637: the not_a_repo answer of the create. 4601: the unsearchable git directory and the could-not-be-examined text. 4493: moved from PROTOCOL.md
	"docs/protocol/git-rules-windows-paths.md":   1593,  // 1593: the reason of a rooted link target, TASK-861, exact count. 1473: each hop of a link, TASK-861, exact count. 1202: the device link and alternates refusals of a remove, TASK-861, exact count. 1182: the scope while no excludes read is stored, and the excludes read item in its own sentence, a review fix of TASK-855 under the decide-alone rule of 2026-11-20, exact count. 1171: the junction as the last name, the read at a symbolic link and at a link above the last name as claustrum's own, and no git call on it, a claims fix of TASK-855 under the decide-alone rule of 2026-11-20, exact count. 1153: no excludes read at a git.status refused for a junction on a share, 4c111e86, and the read at the other refusals as claustrum's own, TASK-855 under the decide-alone rule of 2026-11-20, exact count. 1121: the start test of a remove on an untrusted repository on a share, claustrum's own, a review fix of TASK-854 under the decide-alone rule of 2026-11-20, exact count. 1099: the steps of a remove after a good answer on a share, claustrum's own, a claims fix of TASK-854 under the decide-alone rule of 2026-11-20, exact count. 1076: the trust-refusal exit of a missing path on a share, claustrum's own, a review fix of TASK-856 under the decide-alone rule of 2026-11-20, exact count. 1055: the measured share repository that git answers as no repository, the walk and the start test and .probe steps there as claustrum's own, a review fix of TASK-856 under the decide-alone rule of 2026-11-20, exact count. 999: the calls about the repository of a missing path below a repository on a share, 4c111e86, TASK-856 under the decide-alone rule of 2026-11-20, exact count. 900: the error text of any failed look in the log line, a review fix of TASK-819, exact count. 906: TASK-729 merged with main, exact count. the reparse section cut to its section run, the failed look at .git, review fixes, exact count. 915: the measured include below an IO_REPARSE_TAG_CLOUD folder, a review fix, exact count. 898: the scope of the no-link tags, the remove and create below a WIM folder and the build rule, review fixes, exact count. 826: the new page: the link and share rules moved from git-rules-trust.md, and the reparse tag rules of 4c111e86, the maintainer's split of 2026-11-18, exact count
	"docs/protocol/git-list-branches.md":         1269,  // 1269: no git on the login shell's PATH either, linked, TASK-773 claims fixes under the decide-alone rule of 2026-11-20, exact count. 1252: TASK-808 merged with main, exact count. 1191: the Windows answer with no git marked as claustrum's own, a review fix of TASK-785, exact count. 1188: the no-repository case, the folder and the macOS scope of no git on PATH, a review fix of TASK-785, exact count. 1171: TASK-729 merged with main, exact count. the macOS rows of the git 2.56 gitfile answers, TASK-729, exact count. 1175: the gone git dir of a .git file with git 2.56 on Linux and macOS, 4c111e86, TASK-546 under the decide-alone rule of 2026-11-20, exact count, with the heading of its no-repository list. 1129: the Windows fold of ".." in path, b3081b51. 1107: the \\?\UNC\ and share refusals of b3081b51. 1090: the marker beside a .git folder of b3081b51, also inside the repository. 1047: the marker file or folder and the not-measured edges of the marker walk. 1008: the marker above the path as sent of b3081b51. 1015: the marker folder and the spelling. 995: the page rewrite
	"docs/protocol/git-status.md":                3567,  // 3567: the Windows kept test of 4c111e86 and none on 87375cc7, TASK-773 under the decide-alone rule of 2026-11-20, exact count. 3566: the junction named in the measured .. example, a review nit of TASK-824 under the decide-alone rule of 2026-11-20, exact count. 3561: the claustrum-own mark of a file before a .., a review nit of TASK-824 under the decide-alone rule of 2026-11-20, exact count. 3550: the Windows open test of path before the calls in the git directory, 4c111e86, TASK-824 under the decide-alone rule of 2026-11-20, on top of the 3499 of its walk fold text, exact count. 3500: the junction rev-parse and the NUL excludes rows of TASK-834 on top of the 3495 of main, under the decide-alone rule of 2026-11-20, exact count. 3495: TASK-808 merged with main, exact count. 3486: TASK-729 merged with main, exact count. 3519: the 4c111e86 place of the lfs.storage item on Linux, the decide-alone rule of 2026-11-20, exact count. 3518: the macOS rows of the git 2.56 gitfile answers, TASK-729, exact count. 3519: the git 2.56 gitfile text by system, TASK-546 under the decide-alone rule of 2026-11-20, exact count. 3500: the file link gate of any target repository and its not-measured mark, a review fix, TASK-697 under the decide-alone rule of 2026-11-20, exact count. 3494: the two measured file link rows, TASK-697 under the decide-alone rule of 2026-11-20, exact count. 3468: the symbolic link cases and the measured mark of the order, a review fix, TASK-686 under the decide-alone rule of 2026-11-20, exact count. 3446: the excludes read before the dangling junction answer, a review fix, TASK-686 under the decide-alone rule, exact count. 3435: the walk as sent of a `..` after a junction, claustrum's own, a review fix, TASK-686 under the decide-alone rule of 2026-11-20, exact count. 3412: the build tag of the junction baseRepo status, 4c111e86, exact count. 3408: a claim cut of the claims check (decision-080). 3418: the NUL excludes value of the Windows status call and its exit 128, c9125eb8 (decision-088). 3409: the \\?\UNC\ and share refusals of b3081b51. 3395: a linked worktree as baseRepo. 3302: the network link test of b3081b51. 3254: the lfs.storage item of b3081b51. 3204: the object sources line of b3081b51. 3185: a move between pages, net down in the same PR
	"docs/protocol/git-worktree-create.md":       21697, // 21697: the Linux parent walk and the leaf row of TASK-900 on top of TASK-880 and TASK-881, exact count. 21414: the Windows worktrees file and junction texts of TASK-881 on top of TASK-880, exact count. 21326: the branch create with no sourceBranch marked as own, TASK-880, exact count. 21318: the content check asks again after the 5 s of a kept answer, 87375cc7, TASK-880, exact count after the cuts of TASK-897. 21393: the macOS keep rules of the private LFS store, TASK-883 under the decide-alone rule of 2026-11-20, exact count. 20973: the Windows junction rules of baseRepo, by the decisions of 2026-11-23. 20540: the rows of a stop with files in .claude/, the sends to the commit hash-object, and the Windows reply times scoped to their rows, claims fixes of TASK-889, exact count. 20494: the macOS send counts, the macOS stop of a held listing and the scoped stdin closes of the deadline kill, a review fix of TASK-875, exact count. 20438: the one-shot hash-object after a failed scratch write, a review fix of TASK-875, exact count. 20415: a helper that starts during the kill, the stdin scope and the Windows scratch folder, review fixes of TASK-875, exact count. 20370: the wait for the exit of the killed helpers before the Windows rollback, 87375cc7, TASK-875 under the maintainer decision of 2026-11-23, exact count. 20328: the one send and the reply wait of a killed file hash on Linux and macOS, 87375cc7, TASK-875 under the maintainer decision of 2026-11-23, exact count. 20257: the deadline kill of the four helpers with the measured scope of each system, 87375cc7, TASK-875 under the maintainer decision of 2026-11-23, exact count. 20142: the stop line of a tree hash-object after the kill and the links of the listing test, review fixes of TASK-875, exact count. 20095: the stdin scope, the untracked helper and the LFS keep time of the deadline kill, review fixes of TASK-875, exact count. 20033: the deadline kill of two helpers of the content check, Linux, 4c111e86 and 87375cc7, TASK-875 under the maintainer decision of 2026-11-23, exact count. 19890: the deadline of the content check tested at its next step and the copy-step listing with only worktrees in .claude, 4c111e86 and 87375cc7, TASK-875, exact count. 19851: the measured Windows stop of the content check after 2 objects, its log line only, a claims fix of TASK-864, exact count. 19843: the deadline of the content check scoped to a read that sees it, a claims fix of TASK-864, exact count. 19840: git_lfs_not_found scoped to the dash text, bash as claustrum's own, a review fix of TASK-773 under the decide-alone rule of 2026-11-20, exact count. 19814: the undo text after the git_lfs_not_found text, a review fix of TASK-773 under the decide-alone rule of 2026-11-20, exact count. 19805: the two PATHs of git_not_found and the claustrum-own undo of git_lfs_not_found, TASK-773 claims fixes under the decide-alone rule of 2026-11-20, exact count. 19777: the git_not_found and git_lfs_not_found codes of 87375cc7, TASK-773 and TASK-777, merged with main under the decide-alone rule of 2026-11-20, exact count. 19678: the claustrum-own parts of the listing-only copy step, no manifest pass after a stopped check, and the subject of the 400-byte cap, review fixes of TASK-825 under the decide-alone rule of 2026-11-20, exact count. 19624: the measured scope of the --buffer count and the copy-step listing, and the claustrum-own broken pipe of a hash-object, a review fix of TASK-825 under the decide-alone rule of 2026-11-20, exact count. 19606: the deadline claims of the content check scoped and marked, the --buffer count of a level with one tree, and the content check in the timeoutMs row, a claims fix of TASK-825 under the decide-alone rule of 2026-11-20, exact count. 19558: the deadline stop of the content check, the time of its scratch folder and the config listing of the copy step after it, 4c111e86, TASK-825 and TASK-839 under the decide-alone rule of 2026-11-20, exact count. 19483: the broken pipe cause and the count of the --buffer read of TASK-825, merged with main, exact count. 19447: the pointer to the GIT_ALTERNATE_OBJECT_DIRECTORIES refusal, TASK-857 under the decide-alone rule of 2026-11-20, on top of the 19442 of main, exact count. 19442: the error row of TASK-845 cut to a link to Git filters, and its Linux and macOS rows, from the 19453 of main, under the decide-alone rule of 2026-11-20, exact count. 19453: TASK-823 merged with main (the parent folder of a share baseRepo), exact count. 19452: the hold scope of the settle step and the read-tree under a live hold, and the known gap of its form, a review fix of TASK-832 under the decide-alone rule of 2026-11-20, exact count. 19406: the commit call and the settle step of a create under a live hold of another request with no member, 4c111e86 on macOS, TASK-832 under the decide-alone rule of 2026-11-20, exact count. 19347: TASK-826 merged with main, exact count. 19242: the claustrum-own mark of smudge = cat on Linux and macOS for both states, a review fix of TASK-809, under the decide-alone rule of 2026-11-20, exact count. 19246: the claustrum-own mark of the = refusal narrowed to smudge = cat on Linux and macOS, a review fix of TASK-809, under the decide-alone rule of 2026-11-20, exact count. 19236: the settings-copy state and the measured smudge = cat shape of the Windows exception of a filter name with "=", with its claustrum-own refusals, a review fix of TASK-809, under the decide-alone rule of 2026-11-20, exact count. 19187: the Windows exception of a filter name with "=" whose values all pass, in its error row and its filter bullet, a review fix of TASK-809, under the decide-alone rule of 2026-11-20, exact count. 19157: a name with "=" whose values are all cat gets no item on Windows, 4c111e86, TASK-809 under the decide-alone rule of 2026-11-20, exact count. 19147: the three claustrum-own rules of the lfs arm of the filter check, a review fix of TASK-815, exact count. 19095: TASK-738 merged with main, exact count. 19046: TASK-721 merged with main, exact count. 18940: the claustrum-own rules moved to the end of their bullet and the list of causes cut, a claims fix, exact count. 18946: the four claustrum-own rules of the scratch folder, a review fix, TASK-730 under the decide-alone rule of 2026-11-20, exact count. 18878: the merge with main after PR 1257, both histories kept, exact count. The TASK-730 side: 18725: the scratch files of the content check and their kill row, TASK-730 under the decide-alone rule of 2026-11-20, exact count, merged with main after PRs 1252 and 1255. 18728: with the kill row of the scratch folder on all three systems, a review fix. 18684: the scratch files of the long-running hash-object of 4c111e86 in place of object-body, its start order and the evidence marks of each system, TASK-730, exact count. The TASK-705 side of main: 18788: the whole worktreePath in the Windows mkdir row, a review fix of TASK-705, exact count. 18776: the claims-check fixes of TASK-705, one copy of the timeoutMs wait cut, exact count. 18785: the wait at the add that counts toward timeoutMs in the request row, the Windows mkdir text of a second create of one leaf and the Linux and macOS stale step that removes no entry with the leaf there, claustrum's own, review fixes of TASK-705 (the fix's own text) under the decide-alone rule of 2026-11-20, exact count. 18720: the text of a copy that went while a create waited for the add of another create, the calls of a second create during that add and the wait that counts toward timeoutMs, 4c111e86 and claustrum's own, TASK-705 under the decide-alone rule, exact count. 18635: the macOS rows of the git 2.56 gitfile answers, TASK-729, exact count. 18636: the merge with main after the git 2.56 gitfile answers of TASK-546, exact count. 18644: the extra rev-parse --git-dir of 4c111e86 in the Windows gone .git file create, a review fix of TASK-635 under the decide-alone rule, exact count. 18631: the error and no-error answers of the call of the Windows gone .git file create, a claims fix of TASK-635, exact count. 18622: the measured mark and the three claustrum's own branches of the Windows start test of a gone .git file, a review fix of TASK-635, exact count. 18592: the shadow scope of the Windows start test of a .git file to a missing path, a claims fix, exact count. 18586: the Windows start test of a .git file to a missing path, 4c111e86, TASK-635 under the maintainer's decide-alone rule of 2026-11-20, exact count. 18566: the shadow scope of two Windows and checkout swap items, the swap and worktrees refusals with a daemon GIT_COMMON_DIR and the failed mkdir of worktrees as claustrum's own, TASK-715 under the maintainer's decide-alone rule of 2026-11-20, exact count. 18539: the macOS scope of the own list, a review fix, TASK-718 under the maintainer's decide-alone rule of 2026-11-20, exact count. 18533: no pass at time zero as claustrum's own and the old 1.0 s row tied to the 2 s pass, review fixes, exact count. 18522: the 2 s keep tick during the checkout, 4c111e86, on top of main, exact count. 18515: the shadow scope of the link at .git during the add, under the maintainer's exception of 2026-11-20 for TASK-712, exact count, do not raise again. 18513: the row 112 handle clause restored from main, under the maintainer's exception of 2026-11-20 for TASK-712, exact count, do not raise again. 18507: the maintainer's exception of 2026-11-20 for TASK-712 (the kept state after the link and the link before the request), exact count, do not raise again. 18479: the maintainer's exception of 2026-11-20 for TASK-712 (the row 94 qualifier restored and the no-member state marked), exact count, do not raise again. 18462: the shadow scope of the three swaps that move the hold, a review fix, exact count. 18460: the off-mode states after the answer and after the add, a review fix, exact count. 18462: two claim fixes of the claims check, exact count. 18466: the 5 words of the ProjFS bullet of main after the rebase, the maintainer's word of 2026-11-19, exact count. 18461: the maintainer's exception of 2026-11-18 for TASK-680 (restored rollback rules and own choices), do not raise again. 18309: the space stop poll measured on Linux only, a claims fix of the pin of 4c111e86, exact count (decision-062). 18311: the folder of the daemon and the scope of a seed copy whose file cannot be removed, a review fix, exact count (decision-062). 18284: a stopped seed copy whose file cannot be removed, 4c111e86, exact count (decision-062). 18156: the LFS timeout answer, the keep at the exit of git and the seed end, 4c111e86, merged with main, exact count (decision-062). 18059: the measured smudge-filter and timeoutMs cases of the group kill, evidence words (decision-078). 18055: the group kill after a failed checkout, 4c111e86, merged with main, exact count (decision-062). 17995: the time-limit stop and the dropped bad answer of the commit-id round, and the quoting of the root, review fixes, exact count (decision-062). 17848: the macOS label of the root backticks and the measured scope of the commit-id ask, claim fixes, exact count (decision-062). 17805: the ask and the first location run after an add of a commit id with a worktreeRoot, 4c111e86 and c9125eb8, exact count (decision-062). 17781: the backticks of the root in three worktreeRoot refusals, 4c111e86 and c9125eb8, exact count (decision-062). 17751: the seed bound (timeoutMs + s) / 2, 4c111e86, merged with main, exact count (decision-062). 17717: a skip-worktree file of a sparse checkout, 4c111e86, exact count (decision-062). 17637: an object kept during the checkout stays after the space stop, 4c111e86, exact count (decision-062). 17627: a rollback that waits for the create lock, claustrum's own, exact count (decision-062). 17605: the keep during the checkout, 4c111e86, exact count (decision-062). 17510: two creates of one repository check out at the same time, 4c111e86, and the lock rules as claustrum's own, exact count (decision-062). 17453: the missing-file note of the 404 text, 4c111e86, merged with main, exact count (decision-062). 17434: the end of the text of a failed Git LFS checkout, 4c111e86, merged with main, exact count (decision-062). 17361: the not_kept texts after downloads began, 4c111e86, merged with main, exact count (decision-062). 16895: the couldn't-look text, the mode 0000 .git, the owner rows and their review fixes, 4c111e86, merged with main, exact count (decision-062). 16712: the sha256 test, the filter attribute lfs and the LFS text of a Git LFS file of the content check, 4c111e86, merged with main, exact count (decision-062). 16534: the two listings of the seed, 4c111e86, exact count (decision-062). 16527: the cat-file start before the first id, 4c111e86, exact count (decision-062). 16524: the cat-file start during ls-tree, 4c111e86, on top of the shadow fixes, exact count (decision-062). 16528: 0755 for lfs and lfs/objects and the macOS umask, review fix, on top of the shadow fixes, exact count (decision-062). 16505: the buffered cat-file of the seed with no listing, 4c111e86, on top of the shadow fixes, exact count (decision-062). 16483: the folder of the seed calls, 4c111e86, on top of the shadow fixes, exact count (decision-062). 16468: the shard folder mode 0777 less the umask, 4c111e86, on top of the shadow fixes, exact count (decision-062). 16463: the ls-tree argv of the seed, 4c111e86, on top of the shadow fixes, exact count (decision-062). 16432: the other calls that name a folder of a repository, a claim fix, exact count (decision-062). 16425: the GIT_COMMON_DIR of the --git-dir= calls after a second answer, 4c111e86, exact count (decision-062). 16400: the keep past timeoutMs answers success, 4c111e86, exact count (decision-062). 16378: the rename rule of the keep swap and the second open of the keep copy, claim fixes, exact count (decision-062). 16380: the keep after a deadline and claim fixes of the keep copy, 4c111e86, exact count (decision-062). 16356: the copy of the LFS keep, its .kept- swap and its bound, 4c111e86, exact count (decision-062). 16237: the macOS stop, the read-tree group, the no-signal-after-reap rule, the mode 0300 folder and the stop of the keep, claim fixes, exact count (decision-062). 16216: the same count after the KiB form, the threshold words, the order clause and the cut seed clause, claim fixes, exact count (decision-062). 16216: the stop values of all rows of worktree_lfs_no_space, a claim fix, exact count (decision-062). 16213: the stop of worktree_lfs_no_space and its text, 4c111e86, exact count (decision-062). 15856: no daemon-made leaf folder in .s<n>, 4c111e86, exact count (decision-062). 15840: the mode 0300 row of a staged add, 4c111e86, exact count (decision-062). 15841: no pair with a daemon GIT_COMMON_DIR, a claim fix, exact count (decision-062). 15830: GIT_COMMON_DIR as the last entry of the staged add, 4c111e86, exact count (decision-062). 15820: the listing and the pair before the locked stale refusal, 4c111e86, exact count (decision-062). 15772: the second repository link as claustrum's own, a claim fix, exact count (decision-062). 15771: Linux and Windows of the off-switch row as claustrum's own, a claim fix, exact count (decision-062). 15765: the swap before that listing as from the code, a claim fix, exact count (decision-062). 15761: the three swaps that move the hold and the calls around a swap before the add, 4c111e86, exact count (decision-062). 15741: a link right after the git-dir answer moves the hold, 4c111e86, exact count (decision-062). 15664: the prefix of a swap after the listing of the add, 4c111e86, exact count (decision-062). 15597: the main .git of a linked baseRepo in the pair and the empty tree, 4c111e86, exact count (decision-062). 15560: the git text of a locked leaf with shadowDirOff, 4c111e86, exact count (decision-062). 15543: the measured remove of .s<n> after the 4096 refusal, a claim fix, exact count (decision-062). 15542: the first request before the mode 0100 text, a claim fix, exact count (decision-062). 15538: the measured 0300 row, not built, exact count (decision-062). 15529: the stage folder before the limit refusal, 4c111e86, exact count (decision-062). 15511: the bound of 4096 bytes of an entry HEAD in .s<n>, 4c111e86, exact count (decision-062). 15470: the systems of the gitdir copy, a review fix, exact count (decision-062). 15464: mode 0300 and macOS of the 0100 row, a review fix, exact count (decision-062). 15446: the limit of 4096 worktrees of the staged add, 4c111e86, exact count (decision-062). 15368: the unreadable gitdir of an entry in .s<n>, 4c111e86, exact count (decision-062). 15331: the modes of the files and folders of .s<n>, 4c111e86, exact count (decision-062). 15281: the mode 0100 of the folder of the daemon, 4c111e86, exact count (decision-062). 15265: the macOS rows of the tenth and of the oid name and deadline remove of a seed copy, 4c111e86, exact count (decision-062). 15232: the failed create cut from the oid test of the full read, a claim fix, exact count (decision-062). 15236: the seed copy at the oid name, its remove, the space note after a failed copy and the Linux labels of the full read, 4c111e86, exact count (decision-062). 15158: the claim fixes of the full read and the macOS tenth (decision-062). 15145: the room bound of a seed copy with a tenth of the size, 4c111e86 (decision-062). 15096: the sha256 read of an object with a second link before the seed, 4c111e86 (decision-062). 15062: the lfs.storage item of the settle call, 4c111e86 (decision-062). 15023: the time budget of the keep, claustrum's own, exact count (decision-062). 14998: the record of a create before the add (decision-062). 14996: the pair after a removed stale entry and its resolved work tree, c9125eb8 and 4c111e86 (decision-062). 14905: the claude-ssh-checkout- folder of 4c111e86 (decision-062). 14889: the code worktree_lfs_not_kept, the half bound of the seed of 4c111e86 and the sha256 of the seed copy, exact count (decision-062). 14625: a file or link in place of the leaf of a failed add and the leaf replaced before the add, c9125eb8 and 4c111e86, exact count (decision-062). 14513: the backticks of the relative worktreePath text, 4c111e86 and c9125eb8, exact count (decision-062). 14492: the empty commondir of another entry in the staged copy, 4c111e86 and c9125eb8, exact count (decision-062). 14461: the keep after a failed checkout, 4c111e86, exact count (decision-062). 14413: the bytes test of the keep, 4c111e86, exact count (decision-062). 14378: the leaf of a failed add by its name through a handle of its parent, c9125eb8 and 4c111e86 (decision-062). 14344: the note that claustrum does not build worktree_lfs_not_kept, exact count (decision-062). 14337: the codes worktree_no_space and worktree_quota of 4c111e86, exact count (decision-062). 14084: the stack merge of the create calls and the LFS notes, exact count with review fixes (decision-062). 13854 on 13754: the free space of the keep of 4c111e86, with a claim fix of a review and a review fix of the inferred byte (decision-062). 13801 on 13754: the download words of the ssh note and the words of the missing note, 4c111e86, with claim fixes of a review (decision-062). 13978: a review fix of the systems of the worktreeRoot pair (decision-062). 13980: the commit call and settle step of fallback-git, the work tree of the pair and the location calls again with worktreeRoot, c9125eb8 and 4c111e86 (decision-062). 13840: a review fix of the marker row (decision-062). 13850: the failed close of the marker, c9125eb8 and 4c111e86 (decision-062). 13754: the Windows backticks of the custom-location text of c9125eb8 and 4c111e86 (decision-062). 13753: the 1 GiB spare of the seed and its note, 4c111e86 (decision-062). 13647: the three failure notes of the LFS store of 4c111e86 and the ssh note past the cut (decision-062). 13386: the stale temp sweep of the LFS store of 4c111e86 (decision-062). 13290: the private LFS store of 4c111e86: seed and keep (decision-062). 13116: the unusable-git refusal of 4c111e86 and the systems of each row (decision-062). 12926: the .checkout-<n> folder and the LFS store of 4c111e86 (decision-062). 12847: the ssh value of the member pairs, c9125eb8 and b3081b51. 12844: the Windows order of the door read, the fallback-git member, the member settle ssh value, the Windows pair, the door-read refusal and the live-hold ref as claustrum's own, a claim fix of a review. 12726: the door read before the second listing, the pair after the add listing, one depth read after a fault, the settle step with the member and the no-pair row with the member, with their systems, c9125eb8. 12608: the claustrum-own separate runs and branch step of the ask, a claim fix of a review (decision-080). 12561: the claustrum-own states of the ask before the content check and its cause in the stop line, claim fixes of a review (decision-080). 12523: the not-a-repository text and the ask before the content check, c9125eb8 (decision-089). 12381: the Linux ask after the add, c9125eb8 (decision-089). 12374: the -b add of the sourceBranch case, a claim fix of a review (decision-080). 12370: the sourceBranch case of the macOS call after the add, a claim fix of a review (decision-080). 12360: the Linux call after the checkout as not built, a claim fix of a review (decision-080). 12346: the macOS call about the repository after the add and its branch text, b3081b51 and c9125eb8 (decision-088). 12255: the prefix and two claustrum-own parts of the second git-dir ask, a claim fix of a review (decision-080). 12229: the second git-dir ask of a create (65 words) with the pair and settle fixes of main (12164), exact merged count (decision-080). 12164: the measured kind of link and the limit of the settle listing, claim fixes of a review (decision-080). 12125: the measured link of 5fd08069 in a sentence of its own, a claim fix (decision-080). 12119: the leaf entry that gives no pair and the measured link of 5fd08069, a claim fix of a review (decision-080). 12105: the pair before the add in a first create, b3081b51 and c9125eb8, a claim fix of a review (decision-080). 12046: the claim fixes of a review of the settle call and the level reads (decision-080). 12023: the leaf listing and heavy profile of the settle call, and the level reads after a stop, b3081b51 and c9125eb8 (decision-080). 11962: the second read of the time left after the settle pair, a claim fix of a review (decision-080). 11946: the limit and gate of the settle pair, a claim fix of a review (decision-080). 11925: the GIT_FLUSH value, the level reads after a stop, the blob-hash refusal and the stop texts, claim fixes of a review (decision-080). 11871: the empty-tree pair before the settle call, b3081b51 and c9125eb8 (decision-080). 11825: the long-running hash-object, its stop line, the blob hash and the core.fileMode read, c9125eb8 (decision-080). 11652: the claustrum-own rules of the content-check batch, claim fix of a review (decision-080). 11579: three facts moved from AGENTS.md (TASK-451). 11541: the other open errors of tables.list and the Linux and macOS pass as claustrum's own. 11509: the reftable junction of the git directory of b3081b51 on Windows. 11482: the unheld rollback through a junction. 11467: the measured rollback through a junction. 11397: a worktree path through a junction, b3081b51. 11419: the Windows walk of baseRepo for the create. 11395: the claustrum's own parts of a held staged leaf. 11359: the held staged leaf and its held parent folder of b3081b51 on Windows. 11337: the missing baseRepo below a marker of b3081b51, its systems and the count first. 11291: the Windows create text and the template backticks. 11262: the create backticks of the in-repo text, b3081b51 on macOS. 11256: the Windows stale step by the path, with a directory link at .git. 11233: the \\?\UNC\ and share refusals of b3081b51. 11219: the Windows text of a staged leaf that names another entry of the git directory or of .s<n>, b3081b51. 11177: the four swap texts of b3081b51 on Windows (the worktrees example cut), their not-measured states and the measured stale step. 10940: the macOS mode of the staged entry of b3081b51, its other umasks and the measured worktrees modes. 10908: the refs copy and the file size of the staged add of b3081b51, and its not-measured outcomes. 10622: the unheld create as claustrum's own. 10602: the examine text of an unsearchable registrations folder of b3081b51 (108 words). 10494: the link to the marker rule. 10491: the marker above the path as sent of b3081b51. 10509: the marker folder and the spelling. 10478: the link to the record of a create. 10465: a long sentence of row 147 split. 10464: the quoting and cut of the already exists text (15 words). 10449: the not-measured edges of the mode and link refusals. 10396: the config call of core.fileMode, core.fileMode and the links outside the worktree, and the mode and link refusals of the content check of b3081b51 (366 words). 10030: the already exists backticks. 9997: the network link test of b3081b51. 9970: the claims fixes of m-19. 9968: the content check of b3081b51. 8843: the alternates line of b3081b51. 8812: macOS for the containment backticks. 8810: the backticks of the containment refusal of be1c1ca5. 8768: the .git file gone after the checkout of be1c1ca5. 8643: the staged add of be1c1ca5. 7829: the ssh door of be1c1ca5. 7375: the sweep on Linux and macOS of be1c1ca5. 7335: the refusal texts of be1c1ca5. 7018: the start of timeoutMs. 6996: the 2.2 s settle wait (claustrum's own). 6951: rewrite to the page template
	"docs/protocol/git-worktree-remove.md":       6467,  // 6467: a residual of the junction trust, TASK-894, exact count. 6463: the Windows hold and the daemon GIT_DIR scoped against the stale notes, a review fix of TASK-861, exact count. 6458: the Windows junction rules of baseRepo, by the decisions of 2026-11-23. 5840: the env refusal sentence scoped to the replies of a gone folder, a review fix of TASK-862 under the decide-alone rule of 2026-11-20, exact count. 5836: the env refusal sentence moved to the end of its paragraph with the claustrum-own mark on its place, TASK-862 under the decide-alone rule of 2026-11-20, exact count. 5828: TASK-854 merged with main (the TASK-860 env refusal of its 5744), exact count. 5820: the measured mark of a local baseRepo and the start test with a daemon GIT_DIR, a review fix of TASK-854 under the decide-alone rule of 2026-11-20, exact count. 5806: the start test below a share and the measured calls of a share host that does not resolve, a claims fix of TASK-854 under the decide-alone rule of 2026-11-20, exact count. 5794: a remove of a missing baseRepo that runs no start test and no git version, 4c111e86 on Windows, TASK-854 under the decide-alone rule of 2026-11-20, on top of the 5736 of main, exact count. 5736: the pointer to the GIT_ALTERNATE_OBJECT_DIRECTORIES refusal and its gone-folder and worktreeRoot scope, TASK-857 under the decide-alone rule of 2026-11-20, on top of the 5722 of main, exact count. 5722: a .git on the way that the daemon cannot look at marked as claustrum's own, TASK-852 under the decide-alone rule of 2026-11-20, exact count. 5711: the measured mark of the top share folder kept, a review fix of TASK-823 under the decide-alone rule of 2026-11-20, exact count. 5707: a deeper share baseRepo and a .git file above it marked claustrum's own, a review fix of TASK-823 under the decide-alone rule of 2026-11-20, exact count. 5688: the subfolder of a share baseRepo in its own sentence, a claims fix of TASK-823 under the decide-alone rule of 2026-11-20, exact count. 5683: the parent folder of a share baseRepo, 4c111e86, TASK-823 under the decide-alone rule of 2026-11-20, exact count. 5678: TASK-830 merged with main, exact count. 5612: TASK-729 merged with main, exact count. the macOS rows of the git 2.56 gitfile answers, TASK-729, exact count. 5585: the worktreeRoot reason of the git 2.56 gitfile text by system, TASK-546 under the decide-alone rule of 2026-11-20, exact count. 5583: the symbolic link out of the other reparse point leaves, a review fix, exact count. 5582: the scope of the other reparse point leaves, a review fix, exact count. 5580: the backticks of a Windows junction leaf, 4c111e86, with cuts on the page, exact count (TASK-703). 5583: the identity home guard of the leaf, TASK-044 (maintainer decision 2026-11-16), exact count. 5569: the backticks of the relative worktreePath text and the colon of the gone-worktree trust refusal, 4c111e86 and c9125eb8, exact count (decision-062). 5520: Linux of the worktreeRoot reason and the scope of the ask, a claim fix of a review (decision-080). 5519: the second git-dir ask with worktreeRoot and its refusal, c9125eb8 (decision-089). 5427: the claustrum-own parts of the second git-dir ask, listed one by one (review). 5332: the claustrum-own limits of the second git-dir ask. 5330: the second git-dir ask and its disagree refusal, c9125eb8 (decision-088). 5263: the location refusals that delete nothing, moved from AGENTS.md (TASK-451). 5258: the error reading text of b3081b51. 5157: the existing worktree folder of a missing daemon GIT_DIR, the gitdir record of a D22 lock, the copy qualifier and the measured lock that counts, b3081b51. 5052: the List denial and the home refusal behind a Windows junction. 4980: a baseRepo with a junction before its last name, b3081b51. 4951: the Linux and macOS scope of a missing baseRepo and the List denial of the walk. 4924: the Windows walk of baseRepo through a junction and its fold of "..". 4902: the template backticks. 4900: the worktreeRoot backticks, paid on the page. 4901: the stray commondir content of b3081b51. 4865: the colon form of the count, b3081b51. 4860: the four success states of a gone worktree, b3081b51. 4728: the \\?\UNC\ and share refusals of b3081b51. 4714: the plain path of a baseRepo that exists. 4701: the marker link, the backticks and the stray commondir text of b3081b51, with Linux and macOS for the stray commondir. 4624: the locked gone entry behind a Windows link. 4599: the failed git-directory listing of b3081b51. 4479: the links at the registrations folder on Windows, b3081b51. 4438: the claims fixes of m-19. 4422: the gone worktree of b3081b51. 4398: the alternates line of b3081b51. 4369: macOS for the containment backticks. 4367: the backticks of the containment refusal of be1c1ca5. 4325: the lock texts of be1c1ca5. 4112: rewrite to the page template
	"docs/protocol/launcher-resolve.md":          3825,  // 3825: the Windows re-arm of the refused line by a resolve with no launcherCheck, 4c111e86, and the measured non-object launcherCheck shapes on Windows with the rest marked claustrum's own, TASK-859 under the decide-alone rule of 2026-11-20, on top of the 3797 of main, exact count. 3797: the 65534 row scoped and the .. after a link, a review fix of TASK-803, under the decide-alone rule of 2026-11-20, exact count. 3780: the four launcherCheck rows of 4c111e86 on Linux (write test first, the login uid, ownerUid 65534 with a digest, the real path behind a link), TASK-803 under the decide-alone rule of 2026-11-20, exact count. 3683: the measured form of sha256 named, a review fix of TASK-804, under the decide-alone rule of 2026-11-20, exact count. 3680: the measured values of orManagedFolder and a non-string path named, a review fix of TASK-804, under the decide-alone rule of 2026-11-20, exact count. 3678: the measured shapes of the Windows launcherCheck, and a null launcherCheck counted as absent, a review fix of TASK-804, under the decide-alone rule of 2026-11-20, exact count. 3650: the measured mark of the Windows launcherCheck answer kept to the shapes of its cell, a review fix of TASK-804, under the decide-alone rule of 2026-11-20, exact count. 3647: the systems of the shape-only read of path and the measured types of the launcherCheck members on Windows, a review fix of TASK-804, exact count. 3644: the Windows answer to any launcherCheck object and its repeat rule, 4c111e86, with its claustrum-own marks, exact count (TASK-804). 3626: the macOS reset rule measured, exact count. 3638: the macOS fault and reset rules labeled by what the rows show, a claims fix, exact count. 3578: the repeat rule of the launcherCheck line on macOS, 4c111e86, exact count. 3565: the fault of a managed refusal on macOS, 4c111e86, exact count. 3534: the repeat rule of the launcherCheck line, Linux 4c111e86, macOS c9125eb8, exact count. 3523: the repeat rule of 4c111e86 on all systems, exact count. 3503: the digits-only mark of the bdi name, a review fix, exact count. 3492: the bdi, no-disk, root-on-NFS and owner texts of 4c111e86, exact count (TASK-606). 3219: the check by handle cut, 4c111e86 never refuses a moved path, exact count (decision of 2026-11-15). 3293: eCryptfs, JFS, ReiserFS and bcachefs trusted, exFAT and NTFS3 refused, claustrum's own, a review fix (decision-062). 3273: the kinds that stay trusted with no row (decision-062). 3255: the closed kind set and the 9p text of 4c111e86 (decision-062). 3096: the execute test before the walk, c9125eb8 (decision-062). 3077: the not_absolute answer with no path on Linux, c9125eb8, and the Windows none rule (decision-062). 3066: the macos_host text of 4c111e86 measured with no path, and the claustrum's-own mark of a resolve with a path, a claim fix. 3062: the file system and owner texts of 4c111e86 (decision-062), the check by handle of claustrum and its claim fixes (decision-090). 2949: the 9p mark of a review (decision-080). 2941: the claim fixes of the links row and the FUSE and SMB rule (decision-080). 2923: the FUSE and SMB texts of c9125eb8 (decision-080). 2890: the links fault of c9125eb8 (decision-080). 2859: the claustrum-own rules of the review of PR 1014 (decision-080). 2758: the link holder folders of the walk (decision-080). 2741: the file check, path and launcherCheck of c9125eb8 (decision-080). 1834: the both-refused and empty processWrapper rules of b3081b51. 1807: the env kinds and the file order of b3081b51. 1738: the value that is not a string, of b3081b51. 1620: rewrite to the page template
	"docs/protocol/plugins-prune.md":             1397,  // 1397: the second and third skip texts, the other skip and the two skips of 4c111e86 on Windows, TASK-834 under the decide-alone rule of 2026-11-20, exact count. 1367: the kept count of a young dir that keep names, c9125eb8 (decision-062). 1353: another term after the dash on every system, a claim fix of a review (decision-080). 1351: the Linux rows of a host-term sibling of another and of the own client id, c9125eb8 (decision-080). 1338: the sibling of another client id in two lines, a claim fix of a review (decision-080). 1332: a host-term sibling that shares this root, a fix of a review (decision-080). 1302: the systems of the sibling and host-term rules, claim fixes of a review (decision-080). 1287: a sibling of the host-term shape on macOS and Windows (decision-080). 1227: the detail of the same-install text, claim fix of a review (decision-080). 1209: the long-root approval moved from AGENTS.md. 1199: the not-measured dial failures. 1186: a sibling at a long socket path. 1084: the page rewrite
	"docs/protocol/process-kill.md":              1405,  // 1405: the refused group KILL of an ended child of 87375cc7 on Linux and macOS, the Linux hold and its unmeasured case before the SIGTERM, TASK-892 under the decide-alone rule of 2026-11-20, exact count. 1321: the pending spawn and the stop of a pending test of 4c111e86 on Windows, TASK-834 under the decide-alone rule of 2026-11-20, exact count. 1300: the other errors of the resume recorded and two words of main restored, a review fix, TASK-716 under the decide-alone rule of 2026-11-20, exact count. 1293: the asleep text of 4c111e86 and its trigger, the build of the asleep end and the no-job asleep child, a review fix, exact count, raised by the maintainer's decide-alone rule of 2026-11-20 for TASK-716. 1248: the bound scope of the reap grace, the asleep cases of no job and no wait handle, the asleep text over a failed join and a heading for the run limit, a review fix, exact count after cuts, raised by the maintainer's decide-alone rule of 2026-11-20 for TASK-716. 1237: the failed resume of the start test and its end, followed (TASK-716), the maintainer allows 1237, exact count. 1235: the label of the kill of a child in no job, a review fix, exact count. 1237: the no-job child of a failed or pending start test, a review fix, exact count, at the maintainer's exception. 1236: claim fixes of the claims check, exact count. 1237: the maintainer's exception of 2026-11-18 for the job start test of TASK-694, do not raise again. 1072: the pointer to the given-up id of 4c111e86 and its byte bound (decision-062). 1065: the live-pid skip of the drain kill. 1042: the KILL in the exit drain of b3081b51. 931: the page template
	"docs/protocol/process-killandwait.md":       1005,  // the pointer to the given-up id of 4c111e86 and its byte bound (decision-062). 994: the TERM rule of b3081b51 in the exit drain. 977: the page template
	"docs/protocol/process-link-bound.md":        656,   // 656: the claims fixes of the unattended end, 87375cc7, TASK-853 under the decide-alone rule of 2026-11-20, exact count. 655: the unattended end, 87375cc7, TASK-853 with TASK-766 under the decide-alone rule of 2026-11-20, exact count. 642: the Windows end as a kill, a review fix, exact count. 644: a reattach stops the clock and the cut id of the ending line, c9125eb8 and 4c111e86, exact count (decision-062). 740 before
	"docs/protocol/process-reattach.md":          1050,  // the pointer to the given-up id of 4c111e86 and its byte bound (decision-062). 1046: attach, fresh and proof
	"docs/protocol/process-git-read.md":          1391,  // cliTempRootEnd in the combine text, 4c111e86, exact count (decision-062). 1374: the place of the placeholder entry of the git child, 4c111e86, exact count (decision-062). 1357: the pty key row narrowed to gitRead (decision-062). 1352: the placeholder entry of the git child, claustrum's own (decision-062). 1339: the diff --no-color HEAD shape of c9125eb8 and 4c111e86 (decision-062). 1318: the measured NUL, 0x02, 0x1b and 0x1f refs and the side test in every slot, claim fixes of a review (decision-080). 1294: the systems of a range side of 4096 bytes, claim fix of a review (decision-080). 1285: the claim fixes of log with <A>...<B>, the range in the other diff shapes and the pathspec paths, c9125eb8 (decision-080). 1275: the bound of each side of a range, c9125eb8 (decision-080). 1250: the daemon environment after an adopt, c9125eb8 (decision-080). 1227: the macOS mark and the bound of a range, claim fixes of a review (decision-080). 1215: the claim fixes of the gitRead rows of c9125eb8: the measured systems of each rule, the spent id and the not-measured slots (decision-080). 1200: the new-page limit before
	"docs/protocol/process-spawn.md":             10341, // 10341: some runs of the race name the second SIGKILL, TASK-901, exact count; 10339: the SIGTERM line of the launcher end (Linux) after its SIGKILL and none at a shutdown, 87375cc7, TASK-891, exact count. 10289: the second group SIGKILL of 87375cc7 in the launcher hold recorded as a race, TASK-891, exact count. 10271: the macOS and unmeasured marks of the refused-kill rules, a review fix of TASK-891, exact count. 10255: the Linux scope of the refused-end line, a review fix of TASK-891, exact count. 10240: the lines of a refused group SIGKILL of the launcher end and of the unattended end, and its cleanup count, 87375cc7, TASK-891, exact count. 10164: the scope of the -keep-children gap and of a give-back after a mark, review fixes of TASK-878, exact count. 10143: the systems of a launcherCheck, the Windows launcher row, the key text with no line and the scope of the agent step, TASK-876, exact count. 10105: the scope of a SIGTERM error other than EPERM, TASK-872, exact count. 10100: the keyed pty text of main with the refused SIGTERM of TASK-872, exact count. 9940: the measured pty member and given-up id rows, TASK-870, exact count. 9947: the scope of the log line of linkBound with a launcher, TASK-870, exact count. 9933: the failed-start line of linkBound with a launcher, 4c111e86 and 87375cc7, TASK-870, exact count. 9925: the claims fixes of the keyed pty refusal on Windows, TASK-870, exact count. 9854: the refusal of a keyed pty on Windows, 4c111e86 and 87375cc7, TASK-870, exact count. 9594: a gitRead spawn skips the SSH agent step, with its measured macOS rows, 87375cc7 and 4c111e86, TASK-865, exact count. 9503: the WARN line of a failed removal of CLAUDE_SSH_LEAVE_GROUPS, TASK-765, exact count. 9486: the claims of the launcher group end scoped to their rows, TASK-765, exact count. 9424: the one group SIGKILL also covers a client KILL after the exit, TASK-765, exact count. 9409: one group SIGKILL of the end and the shutdown step, TASK-765, exact count. 9393: the group end of an asked launcher and of a leader that exits at a shutdown, TASK-765, exact count. 9343: the group SIGKILL of a shutdown in the wait of an exited launcher, TASK-765, exact count. 9291: the wait on the pipes and the drain from the exit of a launcher, TASK-765, exact count. 9289: the review fixes of the group end of an exited launcher, TASK-765, exact count. 9262: the group of an exited launcher, 87375cc7, TASK-765, exact count. 8825: the Windows launcher refusal with unattendedSeconds 60, a claims fix of TASK-871, exact count. 8811: the check of a value from 2 to 59, and 1 on Windows, and the launcher refusal on macOS and Windows, claims fixes of TASK-871, exact count. 8795: the review round 1 of PR 1305 (the measured step evidence, the own 60 s cap, the client label of a survived kill), TASK-853 under the decide-alone rule of 2026-11-20, exact count. 8753: the review nits of the unattended end (a survived kill, the stayed line, the second link), TASK-853 under the decide-alone rule of 2026-11-20, exact count. 8729: the Windows window of a child that exits before its reap and a heading for the own rules of the end, a review fix of TASK-853 under the decide-alone rule of 2026-11-20, exact count. 8692: the Linux rows, the launcher rule and the review fixes of the unattended end, 87375cc7, TASK-853 under the decide-alone rule of 2026-11-20, exact count. 8638: the unattended end, 87375cc7, TASK-853 with TASK-766 under the decide-alone rule of 2026-11-20, exact count. 8252: the step-2 rows of TASK-834 (the hung check, the pending outsideJob and the f6010b97 dial) on top of the 8211 of main, under the decide-alone rule of 2026-11-20, exact count. 8211: the launcherFault other of the own path, 4c111e86 and 87375cc7 on Linux, with the launcherSuppliesCli mark, TASK-851 under the decide-alone rule of 2026-11-20, exact count. 8200: the launcher-members place kept and every launcher step named, a review fix of TASK-752 under the decide-alone rule of 2026-11-20, exact count. 8192: the place of the unattendedSeconds test before the launcher steps, a review fix of TASK-752 under the decide-alone rule of 2026-11-20, exact count. 8180: the int64 forms of unattendedSeconds and its log line, a review fix of TASK-752 under the decide-alone rule of 2026-11-20, exact count. 8144: the unattendedSeconds refusals of 87375cc7 and their claustrum-own marks, TASK-752 under the decide-alone rule of 2026-11-20, exact count. 7900: the measured time of the second test file, a review fix of TASK-829, under the decide-alone rule of 2026-11-20, exact count. 7883: the reuse of a test for a later name of the same folder, a review nit of TASK-829, under the decide-alone rule of 2026-11-20, exact count. 7864: the in-turn temp tests of 4c111e86 and their macOS mark, TASK-829 under the decide-alone rule of 2026-11-20, exact count. 7848: the 2 s line of TASK-736 merged with main, exact count. 7834: a folder given back during the look of its held check keeps the hold that the look took, as a given-back folder keeps its flock, 4c111e86, exact count (TASK-737, a merge fix under the decide-alone rule of 2026-11-20). 7837: the merge of the kept flock of TASK-737 (7783 on 7778) with the held check of TASK-734 (7832 on 7778), exact count. 7832: a folder given back during the look of its held check can still be made anew, fitted or held at its name, and that hold ends at the result of the look, exact count (TASK-734, a review fix under the decide-alone rule of 2026-11-20). 7801: a spawn of another temp dir that answers gets its folder while a held check does not answer, 4c111e86, with its claustrum-own marks, in place of the known difference of TASK-734 (2 words shorter), and the gap of the lost list within one check (23 words), exact count (TASK-734, decide-alone). 7783 (TASK-737, on 7778): the kept flock of a given-back folder of 4c111e86 and its claustrum-own end and macOS mark, with a cut of 3 words in the same sentence, exact count (TASK-737, decide-alone). 7778: the macOS bullet, the kept flock of a late folder of 4c111e86 and the b3081b51 clause, review fixes, exact count (TASK-726). 7765: the record, line and next spawn of a late cliTempRoot step, 4c111e86, with its claustrum-own give-back and the macOS mark of a review nit, merged with main, the exact count of the fix's own text (TASK-726, decide-alone). 7730: the measured whyNot of the lock-wait gap and a pointer in place of the not-measured sentence, a review fix of TASK-726, exact count. 7735: the subject of the lock-wait sentence named, a claims fix of TASK-726, exact count. 7731: the line of the time limit only for the spawn whose own step hits it, and the known difference of TASK-734, a claims fix, exact count (TASK-726). 7645: the lock-wait rule for any holder of the lock, a review fix of TASK-726, exact count. 7643: no late step for a step that only waits for the step of another temp dir, claustrum's own, a review fix, exact count (TASK-726). 7627: the claustrum-own end of the late time, a claims fix of TASK-726, exact count. 7617: the claustrum-own key of the late list, the path of the temp dir as given, and a heading for the section run of 375, a review fix, exact count (TASK-726). 7582: the answer at once for a temp dir whose step is late, 4c111e86, with its claustrum-own marks, the exact count of the fix's own text (TASK-726, the decide-alone exception of 2026-11-20). 7527 (with "for a held folder" of a review fix), 7523: the line that the name of a private temp folder does not answer and the line after it, 4c111e86, with their claustrum-own marks of a review fix, the exact count of the fix's own text (TASK-698, the decide-alone exception of 2026-11-20). 7400: the hung-check line of the SSH agent hand-off, claustrum's own, with cuts on the page, exact count (TASK-710). 7400: claim fixes of the claims check, exact count. 7401: the maintainer's exception of 2026-11-18 for the job self-test of TASK-694, do not raise again. 7383: the claustrum-own marks of the cap of the change lines, a claim fix of a review (decision-062). 7351: the cap of the change lines of a private temp folder, 4c111e86, exact count (decision-062). 7281: the ends of the checks of a given-back folder and the grid from the first hold, a claim fix of a review (decision-062). 7273: the section of a given-back folder and the ends and bounds of its checks, a review fix (decision-062). 7241: the 2 s grid from the first make and the checks of a given-back folder, 4c111e86, exact count (decision-062). 7228: the claustrum-own marks of the 2 s and the re-held folder, a claim fix of a review (decision-062). 7226: one check of all held folders and a last check after the give-back, merged with main, 4c111e86, exact count (decision-062). 7155: the cap of new private temp folders, merged with main, exact count (decision-062). 6955: the lost list, the loss kinds and the killedBy after a kill, merged with main, exact count (decision-062). 6519: no start line for a spawn refused after the start, 4c111e86, exact count (decision-062). 6509: a give-up during the start, 4c111e86, with its log order, exact count (decision-062). 6379: the failed-stat rule of an entry that exists, a claim fix (decision-062). 6374: a failed test of the temp dir and its three rules on macOS, 4c111e86, exact count (decision-062). 6234: the pty member of a plain spawn, 4c111e86 and c9125eb8, exact count (decision-062). 6148: the placeholder entry of 4c111e86 in the child env order and a heading over the table of names, exact count (decision-062). 6080: the shellenv failure note of c9125eb8 and 4c111e86 before a good read, exact count (decision-062). 6048: the stack merge of the macOS temp folder and the given-up id cap, exact count (decision-062). 5971 on 5910: the drop of old ids and no renewal in a full set, 4c111e86, exact count (decision-062). 5938 on 5910: the cap of 4096 given-up ids and the burst near it, 4c111e86, exact count (decision-062). 5986: the private temp folder and its end on macOS, 4c111e86, exact count (decision-062). 5885: cliTempRootEnd, the rules of a lost temp folder, the group SIGKILL rule and the split of the request table, 4c111e86, exact count (decision-062, decision-099). 5334: the inner t, the link and file refusal, the held-folder count and the two rmdirs of the temp folder, 4c111e86, exact count (decision-062). 5176: the launcherFault of a refusal with no launcherCheck, c9125eb8 (decision-062). 5144: the given-up id of 4c111e86 and its two Linux rows after 4100 give-ups (decision-062). 4898: the macos_host text of 4c111e86 and its claustrum's-own mark on a spawn. 4886: the build mark of the adopted PATH row, claim fix of a review (decision-080). 4885: the PATH of a read again after an adopt, claim fix of a review (decision-080). 4870: the gitRead exception to the adopted base, claim fix of a review (decision-080). 4864: a good adopt sets the base and the no-adopt PATH, claim fix (decision-080). 4863: the pointer to server.adoptEnv, c9125eb8 (decision-080), merged with the files.mkdir row. 4855: the claustrum-own rules of the new members (decision-080). 4826: the launcherCheck, launcherSuppliesCli, envOnlyIfSent and noLoginShell members of c9125eb8 (decision-080). 4513: the approval of the parent test, moved from AGENTS.md (TASK-451). 4505: the end of the hold while the lock is busy. 4492: the late step log of cliTempRoot. 4467: the text of a record that cannot be saved. 4420: the relative names and the shared wait of the cliTempRoot step. 4376: the cliTempRoot inputs and time limit of b3081b51. 4255: the private temp folder of b3081b51. 3615: the temp-folder test of 683cc6a9. 3474: rewrite to the page template
	"docs/protocol/process-stdin.md":             929,   // attach
	"docs/protocol/server-adopt-env.md":          1563,  // 1563: the measured scope of the offset in one line, a local review nit of TASK-831, exact count. 1557: the local offset of the cap line on Linux outside UTC marked as claustrum's own, a review fix of TASK-831, exact count. 1541: the local zone of the two times of the cap line on macOS, 4c111e86, Linux in UTC, and the macOS failure line, a claims fix of TASK-831, exact count. 1544: the local zone of the two times of the cap line, TASK-831, exact count. 1531: the kept limit on every system and the matched count, nits of a review (decision-080). 1521: the HOME on Windows heading, its claustrum-own marks and the systems of the kept limit, claim fixes of a review (decision-080). 1489: the Windows HOME that kept names and no COMSPEC, PATHEXT or PROMPT, c9125eb8 (decision-080). 1475: the cap line after its window, the time of the cap and the running login shell, claim fixes of a review (decision-080). 1457: the list of the claustrum-own rules of the read again and its heading, a review (decision-080). 1315: the Linux marks of the adopted PATH and the failed read, the own new window and the order of adopt and read, claim fixes of a review (decision-080). 1307: the read again at a spawn after an adopt, its floor, its cap line and its failure note, c9125eb8 (decision-080). 1183: the merged count of the name classes and the bridge words of PR 1023 (decision-080). 1042: the merged count of the name classes and the gitRead exception of PR 1024 (decision-080). 1031: the own mark of the second count and the scope of the temp test, claim fixes of a review (decision-080). 1020: the session-bound and temp-folder names, kept, the null params and the test order, c9125eb8, with the claim fixes of a review (decision-080). 916: the good-read and failed-read claim fixes of a review (decision-080). 895: claim fixes of the read again (decision-080). 890: the new read of the login-shell PATH after an adopt and the noLoginShell row, c9125eb8 (decision-080). 820: the untested sent temp-folder entry, claim fix of a review (decision-080). 807: the earlier list of the log line and the frame count, claim fix (decision-080). 801: the frame of the bridge, moved from flags.md (TASK-458), and the claim fixes (decision-080). 781: a new page of c9125eb8 (TASK-457): its word count plus about 200
	"docs/protocol/server.md":                    2694,  // 2694: the SIGTERM case of a refused sweep SIGKILL, TASK-893, exact count. 2672: the macOS and unmeasured marks of the refused-kill rules, a review fix of TASK-891, exact count. 2633: the cleanup count and the shutdown wait after a refused SIGKILL, and the scopes of a claims check, 87375cc7, TASK-891, exact count. 2592: the count of a held launcher group whose kill landed before the shutdown, claustrum's own, TASK-878, exact count. 2571: the pty sentence scoped to what the rows of 87375cc7 show, a claims fix of TASK-889, exact count. 2558 before  // 2558: the pty row scoped to no stream-json arg and an exit before the shutdown, and the sweep scoped to after the reap, a claims fix of TASK-888, exact count. 2548: the 1.5 s kill also after a refused SIGTERM, and the pty rule scoped to an exited leader, a claims fix of TASK-888, exact count. 2541: the 1.5 s kill of a stream-json child whose exited leader left its group, a claims fix of TASK-888, exact count. 2509: the claims fixes of the shutdown drain kill, TASK-877, exact count. 2478: the macOS rows, the pty exclusion and the second kill after a drain KILL, 87375cc7, TASK-877, exact count. 2454: the reuse, outside-holder and second-kill rows of 87375cc7 under the maintainer decision of 2026-11-23, TASK-877, exact count. 2408: the group SIGKILL of a child in its exit drain at a shutdown on Linux, 87375cc7 and 4c111e86, TASK-877, exact count. 2224: the group of an exited launcher, 87375cc7, TASK-765, exact count. 2201: the feature names git.worktree_create.not_found_codes and files.realpath.how_far, 87375cc7, and a heading for them, TASK-751 under the decide-alone rule of 2026-11-20, exact count. 2187: the feature name process.spawn.unattended_seconds, 87375cc7, TASK-853 with TASK-766 under the decide-alone rule of 2026-11-20, exact count. 2176: the Windows shutdown after a failed start test, a review fix, exact count. 2170: the cuts for the job start test of TASK-694, a review fix, exact count. 2176: the pin of 4c111e86, exact count (decision-062). 2174: the failed poll and the EINTR retry of the peer check, 4c111e86, with the blocked accept, exact count (decision-062). 2106: the number of an exited caller that got no line, c9125eb8 and 4c111e86, exact count (decision-062). 2089: the name git.worktree_create.lfs_error_codes of 4c111e86, exact count (decision-062). 2093: out_of_resources of the peer check, its two features and the failed own naming, 4c111e86, exact count with review fixes (decision-062). 1935: one word cut from a long sentence, a review fix (decision-080). 1936: the caller pid of a lookup_failed line (decision-062). 1910: process.spawn.cli_temp_root_end of 4c111e86 (decision-062). 1899: git.worktree_create.own_lfs_store of 4c111e86 on Linux and macOS (decision-062). 1873: process.spawn.refuses_given_up_id of 4c111e86 (decision-062). 1868: git.worktree_create.unusable_git_refused of 4c111e86 (decision-062). 1856: the files.find words of the methods row, exact count. 1851: the two Linux launcher features of 4c111e86 (decision-062). 1836: the pin of c9125eb8. 1834: the pointer to server.adoptEnv, claim fix of a review (decision-080). 1829: the merge of the files.mkdir words with launcher.supplies_cli (decision-080). 1798: launcher.supplies_cli of c9125eb8 (decision-080). 1793: process.spawn.git_read of c9125eb8. 1780: the Windows shutdown reply of b3081b51. 1747: the pin of b3081b51. 1745: the six feature names of b3081b51. 1721: the third stream-json form and the launcher rule of b3081b51. 1702: the shutdown signals of b3081b51. 1548: the pin of be1c1ca5. 1546: git.hooks_null_device. 1535: the lists of 683cc6a9. 1496: four feature names in the example and the job name. the page rewrite
	"docs/protocol/streams.md":                   621,   // 621: the review nits of the unattended end (a survived kill, the stayed line, the second link), TASK-853 under the decide-alone rule of 2026-11-20, exact count. 612: the claims fixes of the unattended end, 87375cc7, TASK-853 under the decide-alone rule of 2026-11-20, exact count. 606: temp_root after a client kill, merged with main, exact count (decision-062). 598: the killedBy temp_root and a heading, 4c111e86, exact count (decision-062). 584: moved from PROTOCOL.md
	"docs/protocol/git-rules-session-folders.md": 2291,  // 2291: no kept failed start test with no git, 87375cc7 on Linux and Windows, TASK-773 under the decide-alone rule of 2026-11-20, exact count. 2222: the excludes read alone tied to the stored value, the test with one and a test of another request marked as claustrum's own, a review fix of TASK-846 under the decide-alone rule of 2026-11-20, exact count. 2209: the stored value, the plain request that keeps the state and the git.info scope of shadowDirOff, a review fix of TASK-846 under the decide-alone rule of 2026-11-20, exact count. 2188: the per-call rule of the excludes read, the start test and the repository calls while no state is kept, 4c111e86 on Linux, with its claustrum-own marks, its one home under its own heading, TASK-846 under the decide-alone rule of 2026-11-20, exact count. 2137: the place of the sessionFoldersOff refusal before the id, launcher and command tests, a review fix of TASK-805, under the decide-alone rule of 2026-11-20, exact count. 2123: the Windows scope of the claustrum-own marks of sessionFoldersOff, a review fix of TASK-805, under the decide-alone rule of 2026-11-20, exact count. 2113: the Invalid params of a process.spawn with "sessionFoldersOff":1 on Windows, 4c111e86, with its claustrum-own marks (TASK-805), under the decide-alone rule of 2026-11-20, exact count. 2078: a gone record written again by a later create, 4c111e86, exact count (decision-062). 2053: the not-a-folder cause of a file at the record folder, 4c111e86, exact count (decision-062). 2030: the systems of the mkdir through a handle, a claim fix, exact count (decision-062). 2028: the mkdir through a handle of <root>, 4c111e86, and a heading (decision-062). 2000: the record of a create before the add, c9125eb8 and 4c111e86 (decision-062). 1960: the marks of a missing folder on Linux, a claim fix of a review (decision-080). 1959: the resolved record of a folder that does not exist yet, c9125eb8 on macOS (decision-080). 1926: the record of the resolved folder of a spawn, b3081b51 and c9125eb8 (decision-080). 1860: the rename and the remove of a record name, moved from AGENTS.md (TASK-451). 1830: the place, spelling and no-pin folder of a create record. 1788: the stack merge. 1760: the record of a create, b3081b51. 1687: the launcher add-dir and letter-case rules, 3 words of a heading. 1666: the retry after a failed rename. 1659: a heading for the create rule. 1656: the spawn shapes that record, b3081b51. 1582: the record that cannot be saved, b3081b51. 1361: the claims fixes of m-19. 1335: the battery rows of b3081b51. 1234: the claims fixes. 1181: cut after the claims check. 1259: the follow-up rows of b3081b51. 1029: the new page

	"docs/REFERENCE-BUILDS.md": 11907, // 11907: the refusal of a keyed pty on Windows, 4c111e86 and 87375cc7, TASK-870, exact count. 11903: the overclaim of the static check of 19f30c46 cut, a review fix of TASK-816, exact count. 11930: the words that the merge of main cut, exact count. 11946: its first limit, exact count after the private names went (TASK-671, TASK-673, maintainer decision 2026-11-17)
}

// The limits of a document that docWordLimits does not name.
const (
	docNewProtocolPageWords = 1200
	docNewPageWords         = 1500
)

// docNoSectionRunLimit has no section-run limit: the ledger is append-only by the maintainer's
// decision of 2026-10-21. Its word limit is a row of docWordLimits. A new build entry raises
// that row by the words of the entry. Nothing else raises a limit.
const docNoSectionRunLimit = "docs/REFERENCE-BUILDS.md"

// docLimitedFiles lists the files that carry word limits: AGENTS.md, CONTRIBUTING.md,
// README.md, SECURITY.md and each Markdown file under docs/.
func docLimitedFiles(t *testing.T) []string {
	t.Helper()
	files := []string{"AGENTS.md", "CONTRIBUTING.md", "README.md", "SECURITY.md"}
	err := filepath.WalkDir("docs", func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
			files = append(files, filepath.ToSlash(p))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestDocsWordLimits: each Markdown file under docs/, AGENTS.md, CONTRIBUTING.md, README.md
// and SECURITY.md has at most the words of its limit. A word is a run
// of characters between white space.
func TestDocsWordLimits(t *testing.T) {
	files := docLimitedFiles(t)
	seen := map[string]bool{}
	for _, f := range files {
		seen[f] = true
		limit, ok := docWordLimits[f]
		if !ok {
			limit = docNewPageWords
			if strings.HasPrefix(f, "docs/protocol/") {
				limit = docNewProtocolPageWords
			}
		}
		if n := len(strings.Fields(docRead(t, docRel(f)))); n > limit {
			t.Errorf("%s has %d words, and its limit is %d: cut the file, do not raise the limit", f, n, limit)
		}
	}
	for f := range docWordLimits {
		if !seen[f] {
			t.Errorf("docWordLimits names %s, which does not exist: delete the row", f)
		}
	}
}

// docSectionRunLimits holds the most words that each page can have between two headings.
// A row is the longest run that the page has now: the limit works as a ratchet. A row goes
// down, never up, without the maintainer's word. A page with no row gets docSectionRunTarget.
var docSectionRunLimits = map[string]int{
	"AGENTS.md":                                804, // 804: the exact D20 wording, a review fix of TASK-711, exact count. 801: the D20 mark of the gone-leader kill, a review fix of TASK-711, exact count. 795: the startup reap bullet of TASK-711, the maintainer's go of 2026-11-20 for this task, exact count. 738: the signal line of a give-up during a start, 4c111e86, with its Windows end. 684: the mkdir identity in the request-folder Never widen line. 670: an empty request folder and the folder of the daemon in the Never widen line, claim fixes, exact count (decision-062). 667: the parent identity of the rmdir at mode 0300 and its macOS window, a review fix, exact count (decision-062). 629: the rmdir of an empty request folder at mode 0300, 4c111e86, exact count (decision-062). 565: the rename rule of the keep swap and the second open of the keep copy, claim fixes, exact count (decision-062). 552: the macOS label of the keep swap, a claim fix, exact count (decision-062). 549: the keep swap of the LFS store, 4c111e86, exact count (decision-062). 522: the macOS stop, the read-tree group, the no-signal-after-reap rule, the mode 0300 folder and the stop of the keep, claim fixes, exact count (decision-062). 514: the threshold and systems of the LFS space stop, a claim fix, exact count (decision-062). 507: the LFS space stop bullet, 4c111e86, exact count (decision-062). 442: the .probe of the folder of the daemon, a claim fix, exact count (decision-062). 436: the git-shadow bullet names the root at `.probe`, a claims fix, exact count (decision-062). 434: the probe deletes through a root at `.probe` (removeProbeEntry), exact count (decision-062). 432: the home guard of the HEAD copy rmdir, exact count (decision-062). 430: the HEAD copy rmdir as an exception of the git-shadow bullet, exact count (decision-062). 425: the HEAD copy folder and the seed rule of copySeed back as its own rule, exact count (decision-062). 400: the seed copy remove as an exception of the git-shadow bullet, exact count (decision-062). 397: pointer index, TASK-451
	"CONTRIBUTING.md":                          402,
	"SECURITY.md":                              559,
	"docs/ARCHITECTURE.md":                     496,
	"docs/DAEMON.md":                           914,  // 914: the spawn scope of the slow temp folder line back with its own mark, a review fix of TASK-736, exact count. 913: the Windows exit line at shutdown, exact count (decision-062). 909: the start-time rule of the child records
	"docs/DIVERGENCES.md":                      727,  // the identity home guard of the worktree leaf deletes in D2, with and without worktreeRoot, TASK-044 (maintainer decision 2026-11-16), exact count. 709 before
	"docs/IMPROVEMENTS.md":                     1506, // 1506: the claims fixes of the shutdown drain kill, TASK-877, exact count. the KILL exception of item 21, b3081b51
	"docs/INSTALL.md":                          757,  // 757: the parity mark of the single-segment row, a claims fix of TASK-675, exact count. 756: the zstd window refusal of 4c111e86 and the systems of its window row, TASK-675, exact count. 736 before
	"docs/PROTOCOL.md":                         627,
	"docs/STYLE.md":                            343,
	"docs/UPSTREAM-TRACKING.md":                3052, // the merge with main after TASK-546, exact count. 3053: the gone .git file item of git.info cut, out of date, a claims fix of TASK-635, exact count. 3096: Linux named beside macOS in that listing item, a review fix of TASK-635, exact count. 3094: the macOS scope of the config listing after the start test on a .git file to a missing path, a claims fix of TASK-635, exact count. 3091: the pin of 4c111e86: the launcher and files.find items name the pin, and the builds of supplies_cli and the ssh door entries, a claims fix. 3093: the seed calls of 4c111e86. 3094: the stale bullet of the shutdown exit line cut, a review fix (decision-062). 3149: the out_of_resources line of the peer check, 4c111e86, a review fix (decision-062). 3144: the private LFS store of 4c111e86 and its seed calls (decision-062). 3104: the files.find line of 4c111e86 (decision-062). 3088: the .checkout-<n> folder and the lfs.storage item of 4c111e86 (decision-062). 3067: the macos_host text of 4c111e86, a triage claim fix. 3064: the triage claim fix of the launcher texts of 4c111e86 (decision-090). 3057: the pin of c9125eb8: the launcher sentence tagged as the pin, the build of cliRemoved, the run-dir test of a stamped orphan. 3051: the launcher sentence of c9125eb8 (decision-080). 3042: the stamped orphan and 3 s wait item, c9125eb8 (decision-080). 3000: the download texts of b3081b51. 3005: the pin of b3081b51: the content check item cut, two pin words added. 6404: the content check of b3081b51 and its commit-id call. 2968: the pin of be1c1ca5: six triage items cut, two pin sentences added. 3169: the staged add of be1c1ca5. 3123: the ssh door of be1c1ca5. 3105: the links of the long-path dials. 3097: the long-path dials of the host cleaner and plugins.prune. 3084: the long socket path. 3061: the sweep on Linux and macOS of be1c1ca5. 3034: the git env bullet. 3005: the worktree texts of be1c1ca5. 2960 before
	"docs/index.md":                            323,  // the Go pin item (decision-076). 314 before
	"docs/protocol/errors.md":                  1860,
	"docs/protocol/files-extract-tar.md":       388,
	"docs/protocol/flags.md":                   824,
	"docs/protocol/git-info.md":                1905,
	"docs/protocol/git-list-branches.md":       382, // 382: no git on the login shell's PATH either in the no-repository list, TASK-773 claims fixes under the decide-alone rule of 2026-11-20, exact count. 371 before
	"docs/protocol/git-rules-calls.md":         830,
	"docs/protocol/git-rules-config.md":        1093, // 1093: the includeIf conditions as measured and the others as from the code, and the HOME of a ~ include as measured, claim fixes of TASK-827 under the decide-alone rule of 2026-11-20, exact count. 1075: the ~/ include kept as written and the refused includeIf file with a bad line, 4c111e86, TASK-827 under the decide-alone rule of 2026-11-20, exact count. 1060: the junction include of b3081b51, its methods and the 0x9000001A follow. 1021: the NUL and stream includes of b3081b51. 1007 before
	"docs/protocol/git-rules-settings-copy.md": 3321,
	"docs/protocol/git-rules-trust.md":         5254,
	"docs/protocol/git-rules-windows-paths.md": 375,  // 375: the scope while no excludes read is stored, and the excludes read item in its own sentence, a review fix of TASK-855 under the decide-alone rule of 2026-11-20, exact count. 364: the junction as the last name, the read at a symbolic link and at a link above the last name as claustrum's own, and no git call on it, a claims fix of TASK-855 under the decide-alone rule of 2026-11-20, exact count. 346: no excludes read at a git.status refused for a junction on a share, 4c111e86, TASK-855 under the decide-alone rule of 2026-11-20, exact count. 314: the error text of any failed look in the log line, a review fix of TASK-819, exact count. 320: the UNC refusals, moved from git-rules-trust.md unchanged
	"docs/protocol/git-status.md":              3255, // the object sources line of b3081b51. 414 before
	"docs/protocol/git-worktree-create.md":     3258, // the row 112 handle clause restored from main, under the maintainer's exception of 2026-11-20 for TASK-712, exact count, do not raise again. 3252: the maintainer's exception of 2026-11-20 for TASK-712 (the kept state after the link), exact count, do not raise again. 3239: the maintainer's exception of 2026-11-20 for TASK-712 (the row 94 qualifier restored and the no-member state marked), exact count, do not raise again. 3222: the off-mode states, a review fix, exact count. 3223: the quoting of the root in the backtick rows, a review fix, exact count (decision-062). 3145: the macOS label of the root backticks, a claim fix, exact count (decision-062). 3130: the backticks of the root in three worktreeRoot refusals, 4c111e86 and c9125eb8, exact count (decision-062). 3100: the root daemon case and the labels of the couldn't-look rows, review fixes, exact count (decision-062). 3062: the couldn't-look row and the owner rows, 4c111e86, exact count (decision-062). 2917: the leaf replaced before the add, c9125eb8 and 4c111e86, exact count (decision-062). 2868: the backticks of the relative worktreePath text, 4c111e86 and c9125eb8 (decision-062). 2847: a review fix of the marker row (decision-062). 2857: the failed close of the marker, c9125eb8 and 4c111e86 (decision-062). 2761: the unusable-git refusal of 4c111e86 and the systems of each row (decision-062). 2571: the Windows create text and the template backticks. 2569: the create backticks of the in-repo text. 2563: the systems of a staged leaf that names another entry. 2559: the quoting and cut of the already exists text. 2544: the already exists backticks. 2511: macOS for the containment backticks. 2509: the backticks of the containment refusal of be1c1ca5. 2467: the .git file row of be1c1ca5. 2410: the rows of be1c1ca5. 2389 before
	"docs/protocol/git-worktree-remove.md":     4438,
	"docs/protocol/launcher-resolve.md":        293,
	"docs/protocol/plugins-prune.md":           258,
	"docs/protocol/process-killandwait.md":     285,
	"docs/protocol/process-spawn.md":           398, // 398: the first-run scope of the gitRead skip in the SSH agent hand-off, now the longest run, TASK-876, exact count. 392: a folder given back during the look of its held check keeps that hold, exact count (TASK-737, a merge fix under the decide-alone rule of 2026-11-20). 395: the hold of a folder given back during the look of its held check, in the note of the claustrum-own rules of a lost folder, exact count (TASK-734, a review fix under the decide-alone rule of 2026-11-20). 375: the claustrum-own rules of the new members (decision-080). 371 before
	"docs/protocol/process-stdin.md":           309,
	"docs/protocol/server.md":                  257, // 257: the feature name process.spawn.unattended_seconds, 87375cc7, TASK-853 with TASK-766 under the decide-alone rule of 2026-11-20, exact count. 256 before.
	"docs/protocol/streams.md":                 510,
}

// docSectionRunTarget is the run limit of a page that has no row in docSectionRunLimits.
const docSectionRunTarget = 250

// docLongestRun returns the most words that the text has between two headings. A word is a
// run of characters between white space, as in TestDocsWordLimits. Heading lines and fenced
// code blocks add no words, and a heading starts a new run.
func docLongestRun(text string) int {
	longest, run := 0, 0
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~"):
			inFence = !inFence
		case inFence:
		case strings.HasPrefix(line, "#"):
			run = 0
		default:
			run += len(strings.Fields(line))
			longest = max(longest, run)
		}
	}
	return longest
}

// docSectionRunProblem returns a message when the page has a run longer than its limit, or
// "" when it has none.
func docSectionRunProblem(page, text string, limits map[string]int) string {
	limit, ok := limits[page]
	if !ok {
		limit = docSectionRunTarget
	}
	if n := docLongestRun(text); n > limit {
		return page + " has " + strconv.Itoa(n) + " words between two headings, and its limit is " +
			strconv.Itoa(limit) + ": add a heading or cut the text, do not raise the limit"
	}
	return ""
}

// TestDocsSectionRuns: no file that TestDocsWordLimits covers, except docNoSectionRunLimit, has more words between two
// headings than its row in docSectionRunLimits, or docSectionRunTarget without a row.
func TestDocsSectionRuns(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range docLimitedFiles(t) {
		seen[f] = true
		if f == docNoSectionRunLimit {
			continue
		}
		if msg := docSectionRunProblem(f, docRead(t, docRel(f)), docSectionRunLimits); msg != "" {
			t.Error(msg)
		}
	}
	for f, n := range docSectionRunLimits {
		if !seen[f] {
			t.Errorf("docSectionRunLimits names %s, which does not exist: delete the row", f)
		}
		if n <= docSectionRunTarget {
			t.Errorf("docSectionRunLimits gives %s a row of %d: a row must be above %d, so delete it", f, n, docSectionRunTarget)
		}
	}
}

// docProtocolSummaryProblem returns a message when the page has no prose paragraph between
// its H1 and its first "## " heading, or "" when it has one.
func docProtocolSummaryProblem(page, text string) string {
	inFence, afterH1 := false, false
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~"):
			inFence = !inFence
		case inFence:
		case strings.HasPrefix(line, "## "):
			return page + " has no summary: put a prose paragraph between the H1 and the first ## heading"
		case strings.HasPrefix(line, "# "):
			afterH1 = true
		case afterH1 && docIsProse(line):
			return ""
		}
	}
	return page + " has no summary: put a prose paragraph between the H1 and the first ## heading"
}

// docNotProseRE matches the start of a line that is not prose: an indented line, a heading,
// a list item, a quote, a table row, an admonition or a tab.
var docNotProseRE = regexp.MustCompile(`^(\s|#|[-*+>|]|\d+[.)]\s|!!!|\?\?\?|===)`)

// docIsProse reports whether a line belongs to a prose paragraph.
func docIsProse(line string) bool {
	return strings.TrimSpace(line) != "" && !docNotProseRE.MatchString(line)
}

// TestDocsProtocolPageSummary: every page under docs/protocol/ has a prose paragraph between
// its H1 and its first ## heading.
func TestDocsProtocolPageSummary(t *testing.T) {
	pages, err := filepath.Glob(filepath.FromSlash("docs/protocol/*.md"))
	if err != nil || len(pages) == 0 {
		t.Fatalf("no protocol pages found: %v", err)
	}
	for _, f := range pages {
		f = filepath.ToSlash(f)
		if msg := docProtocolSummaryProblem(f, docRead(t, docRel(f))); msg != "" {
			t.Error(msg)
		}
	}
}

// TestDocsReadabilityChecksReportBreakingPages: the two checks above report a page that
// breaks them, so that a pass of the real tests means something.
func TestDocsReadabilityChecksReportBreakingPages(t *testing.T) {
	words := func(n int) string { return strings.TrimSpace(strings.Repeat("word ", n)) }
	if docSectionRunProblem("docs/X.md", "# X\n\n## A\n\n"+words(251)+"\n", nil) == "" {
		t.Error("a run of 251 words with no row was not reported")
	}
	if docSectionRunProblem("docs/X.md", "# X\n\n## A\n\n"+words(250)+"\n\n## B\n\n"+words(250)+"\n", nil) != "" {
		t.Error("two runs of 250 words were reported")
	}
	if docSectionRunProblem("docs/X.md", "# X\n\n```\n"+words(900)+"\n```\n", nil) != "" {
		t.Error("words in a fenced block were counted")
	}
	if docSectionRunProblem("docs/X.md", "# X\n"+words(251)+"\n", map[string]int{"docs/X.md": 251}) != "" {
		t.Error("a run at its row was reported")
	}
	if docProtocolSummaryProblem("docs/protocol/x.md", "# x\n\n## Request\n\nText.\n") == "" {
		t.Error("a page with no summary was not reported")
	}
	if docProtocolSummaryProblem("docs/protocol/x.md", "# x\n\n- item\n\n| a | b |\n\n!!! note\n    text\n\n```\ntext\n```\n\n## Request\n") == "" {
		t.Error("a page whose lines before the first ## are not prose was not reported")
	}
	if docProtocolSummaryProblem("docs/protocol/x.md", "# x\n\nIt does a thing.\n\n## Request\n") != "" {
		t.Error("a page with a summary was reported")
	}
}

// TestDocsTestsKeepTheirPrefix: every test of this file is named TestDocs*. CI runs
// them with -run '^TestDocs' on a docs-only pull request, so a test
// with another name would not run there.
func TestDocsTestsKeepTheirPrefix(t *testing.T) {
	src, err := os.ReadFile("docs_consistency_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?m)^func (Test\w*)\(`).FindAllStringSubmatch(string(src), -1) {
		if !strings.HasPrefix(m[1], "TestDocs") {
			t.Errorf("%s does not start with TestDocs, so a docs-only pull request skips it", m[1])
		}
	}
}
