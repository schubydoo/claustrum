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
	// It lists these on Linux only.
	known["launcher.checked_by_handle"] = true
	known["launcher.local_kinds_only"] = true
	// featuresFor adds this one on a daemon with jobs on (Windows).
	known[jobFeature] = true
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
	known := map[string]bool{"files.mkdir.verified_parent": true, "git.worktree.external_root": true, "process.spawn.link_bound": true, "git.worktree_create.own_lfs_store": true, "git.worktree_create.lfs_error_codes": true, "process.spawn.cli_temp_root": true, "process.spawn.cli_temp_root_end": true, "launcher.checked_by_handle": true, "launcher.local_kinds_only": true}
	for _, m := range docMethods() {
		known[m] = true
	}
	for _, f := range capabilityFeatures {
		known[f] = true
	}
	known[jobFeature] = true
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
// document shows the Linux list. On Windows osCapabilityFeatures is empty, so the
// test puts files.mkdir.verified_parent, git.worktree.external_root, process.spawn.link_bound,
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
	want := append([]string(nil), capabilityFeatures...)
	hasExt := false
	for _, f := range want {
		hasExt = hasExt || f == ext
	}
	if !hasExt {
		last := len(want) - 1
		want = append(want[:last:last], "files.mkdir.verified_parent", ext, "process.spawn.link_bound", "git.worktree_create.own_lfs_store", "git.worktree_create.lfs_error_codes", "process.spawn.cli_temp_root", "process.spawn.cli_temp_root_end", want[last])
	}
	if len(launcherOSFeatures) == 0 {
		last := len(want) - 1
		want = append(want[:last:last], "launcher.checked_by_handle", "launcher.local_kinds_only", want[last])
	}
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
	"AGENTS.md":                                  3195, // a file or link in place of the leaf of a failed add, c9125eb8 and 4c111e86, exact count (decision-062). 3183: the temp-folder sites on macOS, exact count (decision-062). 3179: the signal sites of a lost temp folder, 4c111e86, exact count (decision-062, decision-099). 3112: the inner t of the new temp folder, its rmdir and its heading, 4c111e86, exact count (decision-062). 3085: the LFS store sweep of 4c111e86, a new delete site, exact count (decision-062). 3028: the second method that Windows lacks, files.find (decision-062). 3027: the temp folder delete of a request, exact count (decision-062). 3029: pointer index, TASK-451
	"CONTRIBUTING.md":                            1071,
	"README.md":                                  1124, // the same-methods sentence cut, exact count. 1133: 13 are the HTML tokens of the logo <picture> block (maintainer, 2026-10-31)
	"SECURITY.md":                                1617, // the adopt frame takes auth from daemon.token, claim fix (decision-080). 1609: the ssh door of be1c1ca5. 1553 before
	"docs/ARCHITECTURE.md":                       2671, // pluginhostsibling_*.go out of the file map again, exact count. 2672: pluginhostsibling_*.go in the file map (decision-080). 2671: the same-methods sentence cut, exact count. 2682: gitsshdoor.go. 2674: socketroute*.go. 2673: the sweep on Linux and macOS of be1c1ca5. 2667 before
	"docs/DAEMON.md":                             8680, // the Windows exit line at shutdown, exact count (decision-062). 8667: the cut id of the unattended and lost temp folder lines, exact count (decision-062). 8639: no exit line for a child that the shutdown ends, measured on Linux, and the macOS counts, exact count with review fixes (decision-062). 8618: no exit line for a child that the shutdown ends, c9125eb8 and 4c111e86, exact count (decision-062). 8595: the net-only start line and the refusing line of the peer check, 4c111e86 (decision-062). 8527: a long sentence of the id cut split in two, a review fix (decision-080). 8525: the cut of a long id in four process lines of c9125eb8 and 4c111e86 (decision-062). 8455: the merge of the child-record claim fixes (8412) with the stamped-orphan claim fixes of PR 1028 (8383), exact merged count (decision-080). 8412: the measured link and no-record cases of the child record remove, claim fix of a review (decision-080). 8374: the start-only remove of the child record and the cannot-read reap line of c9125eb8 (decision-080). 8340: the supplies start line of c9125eb8 (decision-080). 8317: the remove rules of the child record, claim fix of a review (decision-080). 8225: claim fixes of the stamped orphans and the 3 s wait, c9125eb8 (decision-080). 8207: the long-root approval and the daemon.lock parity, moved from AGENTS.md (TASK-451). 8187: one lsof run after a run that it gave up on, b3081b51. 8180: the refused signal line. 8150: the cleanup word of b3081b51. 8137: the signals at a long root, measured. 8115: the not-measured parts of a long run root. 8090: the long run root of the host cleaner. 7940: the long socket path. 7616: the start-time line. 7541: the temp-folder lines of 683cc6a9. 7493: the new section Lines of connections. 7175: a new page, its words moved from PROTOCOL.md and ARCHITECTURE.md
	"docs/DIVERGENCES.md":                        8215, // the deadline sentence of the opt-in bounds, moved from AGENTS.md (TASK-451). 8199: the lock that counts in the D22 GIT_DIR state. 8193: the missing daemon GIT_DIR state of D22, its gitdir record and the named locked entry, b3081b51. 8152: the Without worktreeRoot scope of the D2 junction test. 8153: the home guard of the handle path of a Windows remove. 8143: the Windows clause of the missing baseRepo cut. 8149: the cut of the held form of D27. 8166: the pin of b3081b51: the measured D27 reply. 8200: D28. 7908: the staged add of be1c1ca5. 7778: the cap that skips the prune after a full disk. 7764: the summary table
	"docs/EXAMPLES.md":                           1025,
	"docs/GLOSSARY.md":                           1297,
	"docs/IMPROVEMENTS.md":                       1599, // the KILL exception of item 21, b3081b51
	"docs/INSTALL.md":                            5733, // the blob remove with no guard, moved from AGENTS.md (TASK-451). 5727: the download texts and progress lines of b3081b51, the no-length row and the scope of the final line rule and the two values of the final-chunk wait. 5712: before them. 5659: first count. 5585: the measured no-fallback rule for an ldd that cannot run. 5573: the measured /bin/ldd fallback of b3081b51. 5568: the not-measured /bin/ldd mark of b3081b51. 5560: the fixed ldd path and environment of b3081b51. 5539: the blob guard of the new prunes. 5531: the measured rows of the prune. 5508: the prune before the fetch of -install. 5307: page rewrite
	"docs/PROTOCOL.md":                           2904, // the files.find row, exact count. 2890: the claim fix of the -32008 row, its link to the state fallback-git (decision-080). 2886: the -32008 row of gitRead in fallback-git, c9125eb8 (decision-080). 2863: the merge of the server.adoptEnv row with the files.mkdir row, exact count. 2855: the parent of files.mkdir in the tilde rule, claim fix of a review (decision-080). 2847: the merge of the files.mkdir row with the launcher rows of main. 2833: the files.mkdir row, exact count. 2835: the files.realpath row and count. 2826: one sentence on the close line. one sentence on a failed write. the error texts, stream frames and flags moved to their own pages
	"docs/STYLE.md":                              1293, // the page rewrite
	"docs/TOUR.md":                               850,  // a new page: its word count plus about 200
	"docs/UPSTREAM-TRACKING.md":                  6360, // the stale bullet of the shutdown exit line cut, a review fix (decision-062). 6415: the out_of_resources line of the peer check, 4c111e86, a review fix (decision-062). 6410: the private LFS store of 4c111e86 and its seed calls (decision-062). 6370: the files.find line of 4c111e86 (decision-062). 6354: the .checkout-<n> folder and the lfs.storage item of 4c111e86 (decision-062). 6333: the macos_host text of 4c111e86, a triage claim fix. 6330: the triage claim fix of the launcher texts of 4c111e86 (decision-090). 6323: the pin of c9125eb8: the launcher sentence tagged as the pin, the build of cliRemoved, the run-dir test of a stamped orphan. 6342: the launcher sentence of c9125eb8 (decision-080). 6333: the stamped orphan and 3 s wait item, c9125eb8 (decision-080). 6291: the missing daemon GIT_DIR in the D22 row. 6284: the junction trust-root item cut, b3081b51. 6313: the download texts of b3081b51. 6386: the pin of b3081b51: the content check item cut, two pin words added. 6411: the network link exception of b3081b51. 6401: the claims fixes of m-19. 6404: the content check of b3081b51 and its commit-id call. 6335: the junction item of b3081b51. 6312: the D28 row. 6285: the pin of be1c1ca5: six triage items cut, three pin sentences added. 6480: the staged add of be1c1ca5. 6418: the ssh door of be1c1ca5. 6400: the links of the long-path dials. 6392: the long-path dials of the host cleaner and plugins.prune. 6379: the long socket path. 6356: the sweep on Linux and macOS of be1c1ca5. 6329: the git env bullet. 6300: the worktree texts of be1c1ca5. 6250: the build of the new prunes. 6234: the pin of 683cc6a9. 6232: the terminal of 683cc6a9. 6230: the build of the open destDir refusal. 6228: the could-not-be-examined text. 6224: words moved from PROTOCOL.md, net down in the same PR
	"docs/index.md":                              325,  // the Go pin item (decision-076). 316: the reader router
	"docs/protocol/errors.md":                    2038, // the systems of the disagree row split by worktreeRoot, a claim fix of a review (decision-080). 2037: the macOS worktreeRoot case of the disagree row, c9125eb8 (decision-089). 2035: the build tag and link of the remove case (review). 2027: the remove case of the disagree row, c9125eb8 (decision-088). 2015: the root as sent and the quoting and cut of the link-limit text. 1978: the link-limit backticks. 1957: the .probe text, 41 words of a new row
	"docs/protocol/files-extract-tar.md":         2463, // the file that Windows cannot read. 2457: the empty folder that Windows cannot read. 2420: the .synced rules of 683cc6a9
	"docs/protocol/files-find.md":                1288, // a new page: its word count (1088) plus 200, 4c111e86
	"docs/protocol/files-mkdir.md":               536,  // the no-/proc and exact-compare rules, claim fix of a review (decision-080). 508: the name test, the parent codes, the real-path check, the umask mode and the errno codes of c9125eb8, exact count (decision-080). 402: the failed line and the errno 36 scope, claim fix of a review (decision-080). 380: claim fixes of the files.mkdir page, exact count (decision-080). 538: a new page: its word count plus about 200, c9125eb8
	"docs/protocol/files-list.md":                550,  // the ELOOP tries of b3081b51. 468: the network links of 683cc6a9
	"docs/protocol/files-read.md":                825,
	"docs/protocol/files-stat.md":                514,   // the ELOOP tries of b3081b51. 434: the page rewrite
	"docs/protocol/files-validate.md":            445,   // the ELOOP tries of b3081b51
	"docs/protocol/flags.md":                     2738,  // the token pipe before the markers, a review fix (decision-062). 2730: the daemon environment drops the launcher marks, c9125eb8 and 4c111e86, exact count with a review fix (decision-062). 2653: the =1 gate and the relayed-line auth, claim fix (decision-080). 2649: the pointer to the adoptEnv frame of the bridge, c9125eb8 (decision-080). 2639: the prune after a good install. 2633: moved from PROTOCOL.md
	"docs/protocol/git-info.md":                  1998,  // the \\?\UNC\ and share refusals of b3081b51. 1983: the network link test of b3081b51. 1947: the link chain row of its own (Linux and macOS run no link test). 1889: the link chain of git.info, b3081b51. 1881: the alternates row of b3081b51. 1850: the page rewrite
	"docs/protocol/git-rules-calls.md":           5778,  // the held create and the record-mismatch pair as claustrum's own, a claim fix of a review (decision-080). 5732: the two pairs of a create with the member outside fallback-git get false, c9125eb8 and b3081b51. 5720: the settle pair in the limit rule, a claim fix of a review (decision-080). 5715: the Windows sentence of the missing baseRepo cut. 5724: the count of a missing baseRepo below a marker. 5713: the colon form of the count and the start test of a gone remove, b3081b51. 5660: the failed read-tree call of b3081b51 with a child on both pipes past the deadline, Linux and macOS. 5625: the failed read-tree call of b3081b51 with a child on stdout, the drain and a short stderr hold. 5535: the ssh door entries of be1c1ca5. 5276: the trace and ssh entries. 5145: the core.hooksPath value. 5121: moved from PROTOCOL.md
	"docs/protocol/git-rules-config.md":          3214,  // every other shallow link as claustrum's own. 3187: the shallow link of b3081b51 on Windows. 3128: the config link to a pipe of b3081b51, and the includes as claustrum's own. 3082: the junction include of b3081b51, its methods, the 0x9000001A follow and the walk as claustrum's own. 2981: the other colons and reserved names as claustrum's own. 2943: the NUL and stream includes of b3081b51. 2929: the listing of b3081b51 over 64 MiB and the read while git writes it. 2884: moved from PROTOCOL.md
	"docs/protocol/git-rules-settings-copy.md":   3974,  // the git-shadow flock errors that go on, c9125eb8, both name forms (decision-100), and the claustrum-own cases (decision-062). 3856: the .checkout-<n> folder of 4c111e86 and its sweep (decision-062). 3761: the merge of the Linux ask and the 8 calls, exact merged count (decision-080). 3758: the staged row and the claustrum-own parts of the 8 calls, a claim fix of a review (decision-080). 3736: the 8 calls of a remove after a second disagree answer, c9125eb8 (decision-089). 3705: the ask before the content check after the add of a commit, c9125eb8 (decision-089). 3704: the Linux ask after the add, c9125eb8 (decision-089). 3702: the -b add scope of the macOS ask, a claim fix of a review (decision-080). 3696: the sourceBranch scope and anchor of the macOS ask, a claim fix of a review (decision-080). 3689: the macOS ask after a good add of a create, c9125eb8 (decision-088). 3678: the lock-file and privilege scope of the reason alone, and the claustrum-own printed path of Linux, macOS and a relative answer. 3630: the Windows scope of the lock text and the printed path. 3591: the claustrum-own kinds and places of a file that is not a folder. 3536: the error-line join, the measured setup calls of the version text and the FIFO or device file of git.info and git.status of b3081b51, and the regular file of git.status as claustrum's own. 3444: the ended first config --get, and its exception in the killed row. 3439: the ended first config --get of the start test, b3081b51. 3411: the tree of mktree in the start test, b3081b51. 3363: the .readback-objects-<n> entry of b3081b51 and a heading. 3340: the start-test cannot-run rule of b3081b51. 3300: the third run of the call about the repository. the folder .probe, new rules
	"docs/protocol/git-rules-trust.md":           5811,  // the commondir value ./. and the link to the unusable-git refusal of 4c111e86 (decision-062). 5782: the stale start sentence cut. 5801: the missing daemon GIT_DIR, the .git file to a regular file and the gone worktree with git 2.56, b3081b51. 5720: the \\?\UNC\ and share refusals of b3081b51. 5521: the M4 OS error reason of b3081b51, and the other OS errors of S2 and M4 as claustrum's own. 5494: the order and the cleaned path of the network link text. 5472: the network link test of b3081b51. 5254: the gone worktree and the link test of git.info, b3081b51. 5207: the object sources on Windows of b3081b51. 4642: the 1 MiB commondir reason of be1c1ca5. 4637: the not_a_repo answer of the create. 4601: the unsearchable git directory and the could-not-be-examined text. 4493: moved from PROTOCOL.md
	"docs/protocol/git-list-branches.md":         1129,  // the Windows fold of ".." in path, b3081b51. 1107: the \\?\UNC\ and share refusals of b3081b51. 1090: the marker beside a .git folder of b3081b51, also inside the repository. 1047: the marker file or folder and the not-measured edges of the marker walk. 1008: the marker above the path as sent of b3081b51. 1015: the marker folder and the spelling. 995: the page rewrite
	"docs/protocol/git-status.md":                3408,  // a claim cut of the claims check (decision-080). 3418: the NUL excludes value of the Windows status call and its exit 128, c9125eb8 (decision-088). 3409: the \\?\UNC\ and share refusals of b3081b51. 3395: a linked worktree as baseRepo. 3302: the network link test of b3081b51. 3254: the lfs.storage item of b3081b51. 3204: the object sources line of b3081b51. 3185: a move between pages, net down in the same PR
	"docs/protocol/git-worktree-create.md":       14625, // a file or link in place of the leaf of a failed add and the leaf replaced before the add, c9125eb8 and 4c111e86, exact count (decision-062). 14513: the backticks of the relative worktreePath text, 4c111e86 and c9125eb8, exact count (decision-062). 14492: the empty commondir of another entry in the staged copy, 4c111e86 and c9125eb8, exact count (decision-062). 14461: the keep after a failed checkout, 4c111e86, exact count (decision-062). 14413: the bytes test of the keep, 4c111e86, exact count (decision-062). 14378: the leaf of a failed add by its name through a handle of its parent, c9125eb8 and 4c111e86 (decision-062). 14344: the note that claustrum does not build worktree_lfs_not_kept, exact count (decision-062). 14337: the codes worktree_no_space and worktree_quota of 4c111e86, exact count (decision-062). 14084: the stack merge of the create calls and the LFS notes, exact count with review fixes (decision-062). 13854 on 13754: the free space of the keep of 4c111e86, with a claim fix of a review and a review fix of the inferred byte (decision-062). 13801 on 13754: the download words of the ssh note and the words of the missing note, 4c111e86, with claim fixes of a review (decision-062). 13978: a review fix of the systems of the worktreeRoot pair (decision-062). 13980: the commit call and settle step of fallback-git, the work tree of the pair and the location calls again with worktreeRoot, c9125eb8 and 4c111e86 (decision-062). 13840: a review fix of the marker row (decision-062). 13850: the failed close of the marker, c9125eb8 and 4c111e86 (decision-062). 13754: the Windows backticks of the custom-location text of c9125eb8 and 4c111e86 (decision-062). 13753: the 1 GiB spare of the seed and its note, 4c111e86 (decision-062). 13647: the three failure notes of the LFS store of 4c111e86 and the ssh note past the cut (decision-062). 13386: the stale temp sweep of the LFS store of 4c111e86 (decision-062). 13290: the private LFS store of 4c111e86: seed and keep (decision-062). 13116: the unusable-git refusal of 4c111e86 and the systems of each row (decision-062). 12926: the .checkout-<n> folder and the LFS store of 4c111e86 (decision-062). 12847: the ssh value of the member pairs, c9125eb8 and b3081b51. 12844: the Windows order of the door read, the fallback-git member, the member settle ssh value, the Windows pair, the door-read refusal and the live-hold ref as claustrum's own, a claim fix of a review. 12726: the door read before the second listing, the pair after the add listing, one depth read after a fault, the settle step with the member and the no-pair row with the member, with their systems, c9125eb8. 12608: the claustrum-own separate runs and branch step of the ask, a claim fix of a review (decision-080). 12561: the claustrum-own states of the ask before the content check and its cause in the stop line, claim fixes of a review (decision-080). 12523: the not-a-repository text and the ask before the content check, c9125eb8 (decision-089). 12381: the Linux ask after the add, c9125eb8 (decision-089). 12374: the -b add of the sourceBranch case, a claim fix of a review (decision-080). 12370: the sourceBranch case of the macOS call after the add, a claim fix of a review (decision-080). 12360: the Linux call after the checkout as not built, a claim fix of a review (decision-080). 12346: the macOS call about the repository after the add and its branch text, b3081b51 and c9125eb8 (decision-088). 12255: the prefix and two claustrum-own parts of the second git-dir ask, a claim fix of a review (decision-080). 12229: the second git-dir ask of a create (65 words) with the pair and settle fixes of main (12164), exact merged count (decision-080). 12164: the measured kind of link and the limit of the settle listing, claim fixes of a review (decision-080). 12125: the measured link of 5fd08069 in a sentence of its own, a claim fix (decision-080). 12119: the leaf entry that gives no pair and the measured link of 5fd08069, a claim fix of a review (decision-080). 12105: the pair before the add in a first create, b3081b51 and c9125eb8, a claim fix of a review (decision-080). 12046: the claim fixes of a review of the settle call and the level reads (decision-080). 12023: the leaf listing and heavy profile of the settle call, and the level reads after a stop, b3081b51 and c9125eb8 (decision-080). 11962: the second read of the time left after the settle pair, a claim fix of a review (decision-080). 11946: the limit and gate of the settle pair, a claim fix of a review (decision-080). 11925: the GIT_FLUSH value, the level reads after a stop, the blob-hash refusal and the stop texts, claim fixes of a review (decision-080). 11871: the empty-tree pair before the settle call, b3081b51 and c9125eb8 (decision-080). 11825: the long-running hash-object, its stop line, the blob hash and the core.fileMode read, c9125eb8 (decision-080). 11652: the claustrum-own rules of the content-check batch, claim fix of a review (decision-080). 11579: three facts moved from AGENTS.md (TASK-451). 11541: the other open errors of tables.list and the Linux and macOS pass as claustrum's own. 11509: the reftable junction of the git directory of b3081b51 on Windows. 11482: the unheld rollback through a junction. 11467: the measured rollback through a junction. 11397: a worktree path through a junction, b3081b51. 11419: the Windows walk of baseRepo for the create. 11395: the claustrum's own parts of a held staged leaf. 11359: the held staged leaf and its held parent folder of b3081b51 on Windows. 11337: the missing baseRepo below a marker of b3081b51, its systems and the count first. 11291: the Windows create text and the template backticks. 11262: the create backticks of the in-repo text, b3081b51 on macOS. 11256: the Windows stale step by the path, with a directory link at .git. 11233: the \\?\UNC\ and share refusals of b3081b51. 11219: the Windows text of a staged leaf that names another entry of the git directory or of .s<n>, b3081b51. 11177: the four swap texts of b3081b51 on Windows (the worktrees example cut), their not-measured states and the measured stale step. 10940: the macOS mode of the staged entry of b3081b51, its other umasks and the measured worktrees modes. 10908: the refs copy and the file size of the staged add of b3081b51, and its not-measured outcomes. 10622: the unheld create as claustrum's own. 10602: the examine text of an unsearchable registrations folder of b3081b51 (108 words). 10494: the link to the marker rule. 10491: the marker above the path as sent of b3081b51. 10509: the marker folder and the spelling. 10478: the link to the record of a create. 10465: a long sentence of row 147 split. 10464: the quoting and cut of the already exists text (15 words). 10449: the not-measured edges of the mode and link refusals. 10396: the config call of core.fileMode, core.fileMode and the links outside the worktree, and the mode and link refusals of the content check of b3081b51 (366 words). 10030: the already exists backticks. 9997: the network link test of b3081b51. 9970: the claims fixes of m-19. 9968: the content check of b3081b51. 8843: the alternates line of b3081b51. 8812: macOS for the containment backticks. 8810: the backticks of the containment refusal of be1c1ca5. 8768: the .git file gone after the checkout of be1c1ca5. 8643: the staged add of be1c1ca5. 7829: the ssh door of be1c1ca5. 7375: the sweep on Linux and macOS of be1c1ca5. 7335: the refusal texts of be1c1ca5. 7018: the start of timeoutMs. 6996: the 2.2 s settle wait (claustrum's own). 6951: rewrite to the page template
	"docs/protocol/git-worktree-remove.md":       5569,  // the backticks of the relative worktreePath text and the colon of the gone-worktree trust refusal, 4c111e86 and c9125eb8, exact count (decision-062). 5520: Linux of the worktreeRoot reason and the scope of the ask, a claim fix of a review (decision-080). 5519: the second git-dir ask with worktreeRoot and its refusal, c9125eb8 (decision-089). 5427: the claustrum-own parts of the second git-dir ask, listed one by one (review). 5332: the claustrum-own limits of the second git-dir ask. 5330: the second git-dir ask and its disagree refusal, c9125eb8 (decision-088). 5263: the location refusals that delete nothing, moved from AGENTS.md (TASK-451). 5258: the error reading text of b3081b51. 5157: the existing worktree folder of a missing daemon GIT_DIR, the gitdir record of a D22 lock, the copy qualifier and the measured lock that counts, b3081b51. 5052: the List denial and the home refusal behind a Windows junction. 4980: a baseRepo with a junction before its last name, b3081b51. 4951: the Linux and macOS scope of a missing baseRepo and the List denial of the walk. 4924: the Windows walk of baseRepo through a junction and its fold of "..". 4902: the template backticks. 4900: the worktreeRoot backticks, paid on the page. 4901: the stray commondir content of b3081b51. 4865: the colon form of the count, b3081b51. 4860: the four success states of a gone worktree, b3081b51. 4728: the \\?\UNC\ and share refusals of b3081b51. 4714: the plain path of a baseRepo that exists. 4701: the marker link, the backticks and the stray commondir text of b3081b51, with Linux and macOS for the stray commondir. 4624: the locked gone entry behind a Windows link. 4599: the failed git-directory listing of b3081b51. 4479: the links at the registrations folder on Windows, b3081b51. 4438: the claims fixes of m-19. 4422: the gone worktree of b3081b51. 4398: the alternates line of b3081b51. 4369: macOS for the containment backticks. 4367: the backticks of the containment refusal of be1c1ca5. 4325: the lock texts of be1c1ca5. 4112: rewrite to the page template
	"docs/protocol/launcher-resolve.md":          3293,  // eCryptfs, JFS, ReiserFS and bcachefs trusted, exFAT and NTFS3 refused, claustrum's own, a review fix (decision-062). 3273: the kinds that stay trusted with no row (decision-062). 3255: the closed kind set and the 9p text of 4c111e86 (decision-062). 3096: the execute test before the walk, c9125eb8 (decision-062). 3077: the not_absolute answer with no path on Linux, c9125eb8, and the Windows none rule (decision-062). 3066: the macos_host text of 4c111e86 measured with no path, and the claustrum's-own mark of a resolve with a path, a claim fix. 3062: the file system and owner texts of 4c111e86 (decision-062), the check by handle of claustrum and its claim fixes (decision-090). 2949: the 9p mark of a review (decision-080). 2941: the claim fixes of the links row and the FUSE and SMB rule (decision-080). 2923: the FUSE and SMB texts of c9125eb8 (decision-080). 2890: the links fault of c9125eb8 (decision-080). 2859: the claustrum-own rules of the review of PR 1014 (decision-080). 2758: the link holder folders of the walk (decision-080). 2741: the file check, path and launcherCheck of c9125eb8 (decision-080). 1834: the both-refused and empty processWrapper rules of b3081b51. 1807: the env kinds and the file order of b3081b51. 1738: the value that is not a string, of b3081b51. 1620: rewrite to the page template
	"docs/protocol/plugins-prune.md":             1367,  // the kept count of a young dir that keep names, c9125eb8 (decision-062). 1353: another term after the dash on every system, a claim fix of a review (decision-080). 1351: the Linux rows of a host-term sibling of another and of the own client id, c9125eb8 (decision-080). 1338: the sibling of another client id in two lines, a claim fix of a review (decision-080). 1332: a host-term sibling that shares this root, a fix of a review (decision-080). 1302: the systems of the sibling and host-term rules, claim fixes of a review (decision-080). 1287: a sibling of the host-term shape on macOS and Windows (decision-080). 1227: the detail of the same-install text, claim fix of a review (decision-080). 1209: the long-root approval moved from AGENTS.md. 1199: the not-measured dial failures. 1186: a sibling at a long socket path. 1084: the page rewrite
	"docs/protocol/process-kill.md":              1072,  // the pointer to the given-up id of 4c111e86 and its byte bound (decision-062). 1065: the live-pid skip of the drain kill. 1042: the KILL in the exit drain of b3081b51. 931: the page template
	"docs/protocol/process-killandwait.md":       1005,  // the pointer to the given-up id of 4c111e86 and its byte bound (decision-062). 994: the TERM rule of b3081b51 in the exit drain. 977: the page template
	"docs/protocol/process-link-bound.md":        644,   // a reattach stops the clock and the cut id of the ending line, c9125eb8 and 4c111e86, exact count (decision-062). 740 before
	"docs/protocol/process-reattach.md":          1050,  // the pointer to the given-up id of 4c111e86 and its byte bound (decision-062). 1046: attach, fresh and proof
	"docs/protocol/process-git-read.md":          1357,  // the pty key row narrowed to gitRead (decision-062). 1352: the placeholder entry of the git child, claustrum's own (decision-062). 1339: the diff --no-color HEAD shape of c9125eb8 and 4c111e86 (decision-062). 1318: the measured NUL, 0x02, 0x1b and 0x1f refs and the side test in every slot, claim fixes of a review (decision-080). 1294: the systems of a range side of 4096 bytes, claim fix of a review (decision-080). 1285: the claim fixes of log with <A>...<B>, the range in the other diff shapes and the pathspec paths, c9125eb8 (decision-080). 1275: the bound of each side of a range, c9125eb8 (decision-080). 1250: the daemon environment after an adopt, c9125eb8 (decision-080). 1227: the macOS mark and the bound of a range, claim fixes of a review (decision-080). 1215: the claim fixes of the gitRead rows of c9125eb8: the measured systems of each rule, the spent id and the not-measured slots (decision-080). 1200: the new-page limit before
	"docs/protocol/process-spawn.md":             6234,  // the pty member of a plain spawn, 4c111e86 and c9125eb8, exact count (decision-062). 6148: the placeholder entry of 4c111e86 in the child env order and a heading over the table of names, exact count (decision-062). 6080: the shellenv failure note of c9125eb8 and 4c111e86 before a good read, exact count (decision-062). 6048: the stack merge of the macOS temp folder and the given-up id cap, exact count (decision-062). 5971 on 5910: the drop of old ids and no renewal in a full set, 4c111e86, exact count (decision-062). 5938 on 5910: the cap of 4096 given-up ids and the burst near it, 4c111e86, exact count (decision-062). 5986: the private temp folder and its end on macOS, 4c111e86, exact count (decision-062). 5885: cliTempRootEnd, the rules of a lost temp folder, the group SIGKILL rule and the split of the request table, 4c111e86, exact count (decision-062, decision-099). 5334: the inner t, the link and file refusal, the held-folder count and the two rmdirs of the temp folder, 4c111e86, exact count (decision-062). 5176: the launcherFault of a refusal with no launcherCheck, c9125eb8 (decision-062). 5144: the given-up id of 4c111e86 and its two Linux rows after 4100 give-ups (decision-062). 4898: the macos_host text of 4c111e86 and its claustrum's-own mark on a spawn. 4886: the build mark of the adopted PATH row, claim fix of a review (decision-080). 4885: the PATH of a read again after an adopt, claim fix of a review (decision-080). 4870: the gitRead exception to the adopted base, claim fix of a review (decision-080). 4864: a good adopt sets the base and the no-adopt PATH, claim fix (decision-080). 4863: the pointer to server.adoptEnv, c9125eb8 (decision-080), merged with the files.mkdir row. 4855: the claustrum-own rules of the new members (decision-080). 4826: the launcherCheck, launcherSuppliesCli, envOnlyIfSent and noLoginShell members of c9125eb8 (decision-080). 4513: the approval of the parent test, moved from AGENTS.md (TASK-451). 4505: the end of the hold while the lock is busy. 4492: the late step log of cliTempRoot. 4467: the text of a record that cannot be saved. 4420: the relative names and the shared wait of the cliTempRoot step. 4376: the cliTempRoot inputs and time limit of b3081b51. 4255: the private temp folder of b3081b51. 3615: the temp-folder test of 683cc6a9. 3474: rewrite to the page template
	"docs/protocol/process-stdin.md":             929,   // attach
	"docs/protocol/server-adopt-env.md":          1531,  // the kept limit on every system and the matched count, nits of a review (decision-080). 1521: the HOME on Windows heading, its claustrum-own marks and the systems of the kept limit, claim fixes of a review (decision-080). 1489: the Windows HOME that kept names and no COMSPEC, PATHEXT or PROMPT, c9125eb8 (decision-080). 1475: the cap line after its window, the time of the cap and the running login shell, claim fixes of a review (decision-080). 1457: the list of the claustrum-own rules of the read again and its heading, a review (decision-080). 1315: the Linux marks of the adopted PATH and the failed read, the own new window and the order of adopt and read, claim fixes of a review (decision-080). 1307: the read again at a spawn after an adopt, its floor, its cap line and its failure note, c9125eb8 (decision-080). 1183: the merged count of the name classes and the bridge words of PR 1023 (decision-080). 1042: the merged count of the name classes and the gitRead exception of PR 1024 (decision-080). 1031: the own mark of the second count and the scope of the temp test, claim fixes of a review (decision-080). 1020: the session-bound and temp-folder names, kept, the null params and the test order, c9125eb8, with the claim fixes of a review (decision-080). 916: the good-read and failed-read claim fixes of a review (decision-080). 895: claim fixes of the read again (decision-080). 890: the new read of the login-shell PATH after an adopt and the noLoginShell row, c9125eb8 (decision-080). 820: the untested sent temp-folder entry, claim fix of a review (decision-080). 807: the earlier list of the log line and the frame count, claim fix (decision-080). 801: the frame of the bridge, moved from flags.md (TASK-458), and the claim fixes (decision-080). 781: a new page of c9125eb8 (TASK-457): its word count plus about 200
	"docs/protocol/server.md":                    2106,  // the number of an exited caller that got no line, c9125eb8 and 4c111e86, exact count (decision-062). 2089: the name git.worktree_create.lfs_error_codes of 4c111e86, exact count (decision-062). 2093: out_of_resources of the peer check, its two features and the failed own naming, 4c111e86, exact count with review fixes (decision-062). 1935: one word cut from a long sentence, a review fix (decision-080). 1936: the caller pid of a lookup_failed line (decision-062). 1910: process.spawn.cli_temp_root_end of 4c111e86 (decision-062). 1899: git.worktree_create.own_lfs_store of 4c111e86 on Linux and macOS (decision-062). 1873: process.spawn.refuses_given_up_id of 4c111e86 (decision-062). 1868: git.worktree_create.unusable_git_refused of 4c111e86 (decision-062). 1856: the files.find words of the methods row, exact count. 1851: the two Linux launcher features of 4c111e86 (decision-062). 1836: the pin of c9125eb8. 1834: the pointer to server.adoptEnv, claim fix of a review (decision-080). 1829: the merge of the files.mkdir words with launcher.supplies_cli (decision-080). 1798: launcher.supplies_cli of c9125eb8 (decision-080). 1793: process.spawn.git_read of c9125eb8. 1780: the Windows shutdown reply of b3081b51. 1747: the pin of b3081b51. 1745: the six feature names of b3081b51. 1721: the third stream-json form and the launcher rule of b3081b51. 1702: the shutdown signals of b3081b51. 1548: the pin of be1c1ca5. 1546: git.hooks_null_device. 1535: the lists of 683cc6a9. 1496: four feature names in the example and the job name. the page rewrite
	"docs/protocol/streams.md":                   598,   // the killedBy temp_root and a heading, 4c111e86, exact count (decision-062). 584: moved from PROTOCOL.md
	"docs/protocol/git-rules-session-folders.md": 1960,  // the marks of a missing folder on Linux, a claim fix of a review (decision-080). 1959: the resolved record of a folder that does not exist yet, c9125eb8 on macOS (decision-080). 1926: the record of the resolved folder of a spawn, b3081b51 and c9125eb8 (decision-080). 1860: the rename and the remove of a record name, moved from AGENTS.md (TASK-451). 1830: the place, spelling and no-pin folder of a create record. 1788: the stack merge. 1760: the record of a create, b3081b51. 1687: the launcher add-dir and letter-case rules, 3 words of a heading. 1666: the retry after a failed rename. 1659: a heading for the create rule. 1656: the spawn shapes that record, b3081b51. 1582: the record that cannot be saved, b3081b51. 1361: the claims fixes of m-19. 1335: the battery rows of b3081b51. 1234: the claims fixes. 1181: cut after the claims check. 1259: the follow-up rows of b3081b51. 1029: the new page
}

// The limits of a document that docWordLimits does not name.
const (
	docNewProtocolPageWords = 1200
	docNewPageWords         = 1500
)

// docNoWordLimit has no word limit: the ledger is append-only by the maintainer's decision of 2026-10-21.
const docNoWordLimit = "docs/REFERENCE-BUILDS.md"

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
// and SECURITY.md, except docNoWordLimit, has at most the words of its limit. A word is a run
// of characters between white space.
func TestDocsWordLimits(t *testing.T) {
	files := docLimitedFiles(t)
	seen := map[string]bool{}
	for _, f := range files {
		seen[f] = true
		if f == docNoWordLimit {
			continue
		}
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
	"AGENTS.md":                                397, // pointer index, TASK-451
	"CONTRIBUTING.md":                          402,
	"SECURITY.md":                              559,
	"docs/ARCHITECTURE.md":                     496,
	"docs/DAEMON.md":                           913, // the Windows exit line at shutdown, exact count (decision-062). 909: the start-time rule of the child records
	"docs/DIVERGENCES.md":                      709,
	"docs/IMPROVEMENTS.md":                     1493, // the KILL exception of item 21, b3081b51
	"docs/INSTALL.md":                          736,
	"docs/PROTOCOL.md":                         627,
	"docs/STYLE.md":                            343,
	"docs/UPSTREAM-TRACKING.md":                3094, // the stale bullet of the shutdown exit line cut, a review fix (decision-062). 3149: the out_of_resources line of the peer check, 4c111e86, a review fix (decision-062). 3144: the private LFS store of 4c111e86 and its seed calls (decision-062). 3104: the files.find line of 4c111e86 (decision-062). 3088: the .checkout-<n> folder and the lfs.storage item of 4c111e86 (decision-062). 3067: the macos_host text of 4c111e86, a triage claim fix. 3064: the triage claim fix of the launcher texts of 4c111e86 (decision-090). 3057: the pin of c9125eb8: the launcher sentence tagged as the pin, the build of cliRemoved, the run-dir test of a stamped orphan. 3051: the launcher sentence of c9125eb8 (decision-080). 3042: the stamped orphan and 3 s wait item, c9125eb8 (decision-080). 3000: the download texts of b3081b51. 3005: the pin of b3081b51: the content check item cut, two pin words added. 6404: the content check of b3081b51 and its commit-id call. 2968: the pin of be1c1ca5: six triage items cut, two pin sentences added. 3169: the staged add of be1c1ca5. 3123: the ssh door of be1c1ca5. 3105: the links of the long-path dials. 3097: the long-path dials of the host cleaner and plugins.prune. 3084: the long socket path. 3061: the sweep on Linux and macOS of be1c1ca5. 3034: the git env bullet. 3005: the worktree texts of be1c1ca5. 2960 before
	"docs/index.md":                            323,  // the Go pin item (decision-076). 314 before
	"docs/protocol/errors.md":                  1850,
	"docs/protocol/files-extract-tar.md":       388,
	"docs/protocol/flags.md":                   824,
	"docs/protocol/git-info.md":                1905,
	"docs/protocol/git-list-branches.md":       371,
	"docs/protocol/git-rules-calls.md":         830,
	"docs/protocol/git-rules-config.md":        1060, // the junction include of b3081b51, its methods and the 0x9000001A follow. 1021: the NUL and stream includes of b3081b51. 1007 before
	"docs/protocol/git-rules-settings-copy.md": 3321,
	"docs/protocol/git-rules-trust.md":         5254,
	"docs/protocol/git-status.md":              3255, // the object sources line of b3081b51. 414 before
	"docs/protocol/git-worktree-create.md":     2917, // the leaf replaced before the add, c9125eb8 and 4c111e86, exact count (decision-062). 2868: the backticks of the relative worktreePath text, 4c111e86 and c9125eb8 (decision-062). 2847: a review fix of the marker row (decision-062). 2857: the failed close of the marker, c9125eb8 and 4c111e86 (decision-062). 2761: the unusable-git refusal of 4c111e86 and the systems of each row (decision-062). 2571: the Windows create text and the template backticks. 2569: the create backticks of the in-repo text. 2563: the systems of a staged leaf that names another entry. 2559: the quoting and cut of the already exists text. 2544: the already exists backticks. 2511: macOS for the containment backticks. 2509: the backticks of the containment refusal of be1c1ca5. 2467: the .git file row of be1c1ca5. 2410: the rows of be1c1ca5. 2389 before
	"docs/protocol/git-worktree-remove.md":     4438,
	"docs/protocol/launcher-resolve.md":        293,
	"docs/protocol/plugins-prune.md":           258,
	"docs/protocol/process-killandwait.md":     285,
	"docs/protocol/process-spawn.md":           375, // the claustrum-own rules of the new members (decision-080). 371 before
	"docs/protocol/process-stdin.md":           309,
	"docs/protocol/server.md":                  256,
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

// TestDocsSectionRuns: no file that TestDocsWordLimits covers has more words between two
// headings than its row in docSectionRunLimits, or docSectionRunTarget without a row.
func TestDocsSectionRuns(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range docLimitedFiles(t) {
		seen[f] = true
		if f == docNoWordLimit {
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
