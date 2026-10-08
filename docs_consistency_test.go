package main

import (
	"os"
	"path/filepath"
	"regexp"
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

// TestDocsEveryMethodHasAHome: each method of server.capabilities has a heading in
// docs/PROTOCOL.md, a table row of the server.* table, or a page in docs/protocol/.
func TestDocsEveryMethodHasAHome(t *testing.T) {
	named := map[string]bool{}
	for _, line := range strings.Split(docRead(t, "docs", "PROTOCOL.md"), "\n") {
		switch {
		case strings.HasPrefix(line, "##"):
			// A heading names the method as its first word, with or without backticks.
			if f := strings.Fields(strings.TrimLeft(line, "#")); len(f) > 0 {
				named[strings.Trim(f[0], "`")] = true
			}
		case strings.HasPrefix(line, "| `"):
			// The server.* methods sit in a table, one method for each row.
			if end := strings.Index(line[3:], "`"); end >= 0 {
				named[line[3:3+end]] = true
			}
		}
	}
	for _, m := range capabilityMethods {
		page := strings.NewReplacer(".", "-", "_", "-").Replace(m)
		_, err1 := os.Stat(filepath.Join("docs", "protocol", page+".md"))
		_, err2 := os.Stat(filepath.Join("docs", "protocol", strings.ToLower(page)+".md"))
		if named[m] || err1 == nil || err2 == nil {
			continue
		}
		t.Errorf("docs/PROTOCOL.md has no section for the method %q: add a \"#### %s\" heading, or a page docs/protocol/%s.md", m, m, page)
	}
}

// docNamespaceRE finds a span that names something in a namespace of the daemon.
var docNamespaceRE = regexp.MustCompile(`^(?:server|files|git|launcher|process|plugins)\.[A-Za-z0-9_.*]+$`)

// docNotMethods lists the spans of the form <namespace>.<name> that are not
// methods and not advertised features. Each entry has its reason.
var docNotMethods = map[string]string{
	"server.version": "the documents name it as the method that 7d193f89 removed",
	"server.go":      "a source file",
	"process.go":     "a source file",
	"git.exe":        "the Windows git program, inside an error text",
}

// TestDocsNoPhantomMethod: a span like `git.foo` in the documents is a method, an
// advertised feature, a wildcard like `git.*`, or an entry of docNotMethods.
func TestDocsNoPhantomMethod(t *testing.T) {
	known := map[string]bool{}
	for _, m := range capabilityMethods {
		known[m] = true
	}
	for _, f := range capabilityFeatures {
		known[f] = true
	}
	// capabilityFeatures lists this one on unix only, and the test must pass on Windows.
	known["git.worktree.external_root"] = true
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

// TestDocsNoPhantomMethodInWirelogComment: the comments of wirelog.go name only
// real methods. Only this file is scanned, because other comments name files.
func TestDocsNoPhantomMethodInWirelogComment(t *testing.T) {
	known := map[string]bool{}
	for _, m := range capabilityMethods {
		known[m] = true
	}
	re := regexp.MustCompile(`\b(?:server|files|git|launcher|process|plugins)\.[a-z][A-Za-z_]*\b`)
	for i, line := range strings.Split(docRead(t, "wirelog.go"), "\n") {
		idx := strings.Index(line, "//")
		if idx < 0 {
			continue
		}
		for _, name := range re.FindAllString(line[idx:], -1) {
			if !known[name] && !strings.HasSuffix(name, ".go") {
				t.Errorf("wirelog.go line %d names the method %q, which does not exist: name a real method", i+1, name)
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
// len(capabilityMethods). The history sentences of PROTOCOL.md and
// REFERENCE-BUILDS.md are not in the list on purpose.
func TestDocsMethodCount(t *testing.T) {
	want := len(capabilityMethods)
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

// TestDocsDivergenceCatalog: the table of the catalog and the entry headings list
// the same live IDs, every heading carries its anchor, and a retired entry is no
// row of the table.
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
}

var (
	docDRE  = regexp.MustCompile(`\bD([1-9][0-9]*)\b`)
	docCTRE = regexp.MustCompile(`\bCT-([1-9][0-9]*)\b`)
)

// TestDocsDivergenceIDsExist: every D<n> and CT-<n> that a document cites is a live
// or retired entry of docs/DIVERGENCES.md. The pattern skips the cells of the
// measurement tables that carry a letter or a leading zero (D2p, D8e, D03, D-12).
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
		if !ok || rel == "index.md" || strings.HasSuffix(f, "requirements.txt") {
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

var docFlagDefRE = regexp.MustCompile(`flag\.[A-Za-z0-9]+\(\s*"([a-z0-9-]+)"`)

// docUndocumentedFlags lists flags that the documents deliberately leave out.
var docUndocumentedFlags = map[string]string{}

// TestDocsEveryFlagIsDocumented: each flag that main.go defines appears as a code
// span `-name` in docs/PROTOCOL.md or README.md, alone or inside a longer span such
// as `-wire-log <path>`.
func TestDocsEveryFlagIsDocumented(t *testing.T) {
	defs := docFlagDefRE.FindAllStringSubmatch(docRead(t, "main.go"), -1)
	if len(defs) < 10 {
		t.Fatalf("main.go: found only %d flag definitions: the way flags are defined changed, update docFlagDefRE in docs_consistency_test.go", len(defs))
	}
	var spans []string
	for _, f := range []string{"docs/PROTOCOL.md", "README.md"} {
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
			t.Errorf("main.go defines the flag -%s, but docs/PROTOCOL.md and README.md have no code span for it: document `-%s` in the flags section of docs/PROTOCOL.md", name, name)
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
	{"docs/UPSTREAM-TRACKING.md", regexp.MustCompile("claustrum follows `([0-9a-f]+)`, the build that `scripts/UPSTREAM_SHA` names")},
	{"docs/UPSTREAM-TRACKING.md", regexp.MustCompile("The build pinned today, `([0-9a-f]+)`")},
	{"docs/REFERENCE-BUILDS.md", regexp.MustCompile("\\| Reference SHA \\| Built \\(UTC\\) \\| Wire changes \\| Reconciled in \\| \\|---\\|---\\|---\\|---\\| \\| `([0-9a-f]+)…`")},
}

// TestDocsPinnedBuild: each place that names the current pinned reference build
// names the build that scripts/UPSTREAM_SHA holds. A ledger hash is the short form
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
