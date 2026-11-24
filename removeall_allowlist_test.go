package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The tree delete test. It parses every production .go file of the module
// and finds each selector named RemoveAll, as a call or as a function value.
// That covers os.RemoveAll and the RemoveAll method of an os.Root. Each site
// must be in treeDeleteSites, keyed by its file and its enclosing function or
// var. A new site fails the test with a message that names the page to read.
// A second test checks that the functions that an entry names still call
// their home guard. The scan reads names only, with no type check, so any
// selector named RemoveAll counts.

// treeDeleteSite is one function or package var of production code that holds
// tree deletes.
type treeDeleteSite struct {
	file  string // path from the module root, with "/" slashes
	fn    string // enclosing function ("Type.method" for a method) or package var
	count int    // number of RemoveAll selectors in it
	why   string // the AGENTS.md Part A item that permits it
	// guards lists the functions that must each call their home guard. An
	// empty list means no home guard is owed, and noGuard says why.
	guards  []homeGuardCall
	noGuard string
}

// homeGuardCall names a function that must call a homeguard function.
type homeGuardCall struct {
	file, fn, call string
}

const (
	wipesHome   = "homeguard.WipesHomeDir"
	folderHolds = "homeguard.FolderHoldsHome"
)

// treeDeleteSites is the one list of the recursive deletes of production code.
var treeDeleteSites = []treeDeleteSite{
	{
		file: "internal/files/methods_files.go", fn: "wipeDestDir", count: 1,
		why:    "Path 1: files.extract_tar wipes the last name of destDir",
		guards: []homeGuardCall{{"internal/files/methods_files.go", "filesExtractTar", wipesHome}, {"internal/files/methods_files.go", "destEntryHoldsHome", folderHolds}},
	},
	{
		file: "internal/files/methods_files.go", fn: "prepareOldDestDir", count: 1,
		why:    "Path 1: an old destDir loses .synced before the wipe",
		guards: []homeGuardCall{{"internal/files/methods_files.go", "filesExtractTar", wipesHome}},
	},
	{
		file: "internal/files/methods_files.go", fn: "extractTarGz", count: 1,
		why:    "the extract marker: .synced goes after a failure, through the tested handle of destDir",
		guards: []homeGuardCall{{"internal/files/methods_files.go", "filesExtractTar", wipesHome}},
	},
	{
		file: "internal/files/methods_files.go", fn: "writeSyncedMarker", count: 1,
		why:    "the extract marker: .synced goes through the tested handle of destDir",
		guards: []homeGuardCall{{"internal/files/methods_files.go", "filesExtractTar", wipesHome}},
	},
	{
		file: "internal/git/worktreeremove.go", fn: "deleteWorktreeDir", count: 3,
		why:    "Path 2: git.worktree_remove deletes worktreePath",
		guards: []homeGuardCall{{"internal/git/methods_git.go", "worktreeRemoveHomeRefusal", wipesHome}, {"internal/git/methods_git.go", "worktreeLeafHomeRefusal", folderHolds}},
	},
	{
		file: "internal/git/worktreeremove.go", fn: "dropWorktreeEntry", count: 1,
		why:    "Path 2: git.worktree_remove deletes the registration of worktreePath",
		guards: []homeGuardCall{{"internal/git/methods_git.go", "worktreeRemoveHomeRefusal", wipesHome}},
	},
	{
		file: "internal/git/worktreeremove.go", fn: "dropMatchingEntry", count: 1,
		why:    "Path 2: git.worktree_remove deletes the registration of worktreePath",
		guards: []homeGuardCall{{"internal/git/methods_git.go", "worktreeRemoveHomeRefusal", wipesHome}},
	},
	{
		file: "internal/git/worktreeremove.go", fn: "dropHeldEntry", count: 1,
		why:    "Path 2: git.worktree_remove deletes the registration through the held git directory",
		guards: []homeGuardCall{{"internal/git/methods_git.go", "worktreeRemoveHomeRefusal", wipesHome}},
	},
	{
		file: "internal/git/worktreeverify.go", fn: "clearWorktreeLeaf", count: 1,
		why:    "Path 3: the git.worktree_create rollback deletes the leaf",
		guards: []homeGuardCall{{"internal/git/worktreeverify.go", "leafHoldsHome", wipesHome}, {"internal/git/worktreeverify.go", "leafHoldsHome", folderHolds}},
	},
	{
		file: "internal/git/gitdirheld.go", fn: "removeHeldEntry", count: 1,
		why: "Path 3: the rollback and the stale step delete one registration through the held git directory",
		guards: []homeGuardCall{
			{"internal/git/gitdirheld.go", "acceptedGitDir.removeRegistration", wipesHome},
			{"internal/git/worktreeverify.go", "removeTestedRegistration", wipesHome},
			{"internal/git/worktreestale_unix.go", "removeStaleRegistration", wipesHome},
			{"internal/git/worktreestale_windows.go", "removeStaleWorktreeEntry", wipesHome},
			{"internal/git/worktreestaging.go", "addStage.removeMoved", wipesHome},
		},
	},
	{
		file: "internal/git/githarden_index_unix.go", fn: "removeCreatedRegistration", count: 1,
		why:     "Path 3: the rollback deletes the registration of a create that holds no git directory",
		noGuard: "AGENTS.md Path 3 names it the one tree delete with no home guard. Its root is the registrations folder that the check verified",
	},
	{
		file: "internal/git/githarden_index_windows.go", fn: "removeCreatedRegistration", count: 1,
		why:     "Path 3: the rollback deletes the registration of a create that holds no git directory",
		noGuard: "AGENTS.md Path 3 names it the one tree delete with no home guard. Its path is the registration that the check resolved",
	},
	{
		file: "internal/git/worktreestaging.go", fn: "addStage.removeOwnStaging", count: 1,
		why:    "Path 3: one of the two staging removes of the rollback",
		guards: []homeGuardCall{{"internal/git/worktreestaging.go", "addStage.removeOwnStaging", wipesHome}},
	},
	{
		file: "internal/git/worktreestaging.go", fn: "removeStagingEntry", count: 1,
		why:    "Path 4: the staging sweep removes old staging entries",
		guards: []homeGuardCall{{"internal/git/worktreestaging.go", "sweepOldStaging", wipesHome}},
	},
	{
		file: "internal/install/install.go", fn: "stageAndInstall", count: 1,
		why:    "Path 5: -install deletes a folder at the CLI path",
		guards: []homeGuardCall{{"internal/install/install.go", "stageAndInstall", folderHolds}},
	},
	{
		file: "internal/git/gitshadow.go", fn: "ShadowHost.removeEntry", count: 1,
		why:    "git-shadow: a delete through the root at <socket folder>/git-shadow",
		guards: []homeGuardCall{{"internal/git/gitshadow.go", "ShadowHost.removeEntry", wipesHome}},
	},
	{
		file: "internal/git/gitshadowprobe.go", fn: "removeProbeEntry", count: 1,
		why:    "git-shadow: a delete through the root at the .probe of the folder of the daemon",
		guards: []homeGuardCall{{"internal/git/gitshadowprobe.go", "removeProbeEntry", wipesHome}},
	},
	{
		file: "internal/git/gitshadowuse.go", fn: "removeTempTree", count: 1,
		why:    "git-shadow: a temp folder of a request goes by removeTempRequestDir",
		guards: []homeGuardCall{{"internal/git/gitshadowuse.go", "removeTempRequestDir", wipesHome}},
	},
	{
		file: "methods_plugins.go", fn: "pluginRemoveAll", count: 1,
		why:     "plugins.prune deletes <socket-derived-root>/<hash>",
		noGuard: "the root comes from the socket and the leaf matches files.PluginHashRE. The path is never caller-supplied and never ~-expanded",
	},
	{
		file: "internal/hostsweep/hostclean.go", fn: "hcRemoveAll", count: 1,
		why:     "the host cleaner removes a run dir by one os.Root.RemoveAll at <root>/run",
		noGuard: "the root is <root>/run and the name comes from a directory read, never from a request value or an operator path",
	},
}

// treeDeleteFound is one RemoveAll selector found in the source.
type treeDeleteFound struct {
	file, fn string
	line     int
}

// productionGoFiles lists every non-test .go file below the module root. It
// skips testdata folders and folders whose name starts with "." or is
// "scratch", which hold no production code.
func productionGoFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != "." && (name == "testdata" || name == "scratch" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cannot walk the module: %v", err)
	}
	return files
}

// funcDeclName names a function "f", or a method "T.m".
func funcDeclName(d *ast.FuncDecl) string {
	if d.Recv != nil && len(d.Recv.List) == 1 {
		typ := d.Recv.List[0].Type
		if s, ok := typ.(*ast.StarExpr); ok {
			typ = s.X
		}
		if g, ok := typ.(*ast.IndexExpr); ok {
			typ = g.X
		}
		if id, ok := typ.(*ast.Ident); ok {
			return id.Name + "." + d.Name.Name
		}
	}
	return d.Name.Name
}

// scanTreeDeletes returns every RemoveAll selector of files, with its
// enclosing declaration, and for each declaration the homeguard functions
// that it calls.
func scanTreeDeletes(t *testing.T, files []string) ([]treeDeleteFound, map[string]map[string]bool) {
	t.Helper()
	var found []treeDeleteFound
	guards := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("cannot parse %s: %v", file, err)
		}
		for _, d := range f.Decls {
			// A var block can hold several specs. Walk each spec on its own so
			// that a site keys on its own var.
			type part struct {
				name string
				node ast.Node
			}
			var parts []part
			if g, ok := d.(*ast.GenDecl); ok {
				for _, s := range g.Specs {
					if vs, ok := s.(*ast.ValueSpec); ok {
						parts = append(parts, part{vs.Names[0].Name, vs})
					}
				}
			} else if fd, ok := d.(*ast.FuncDecl); ok {
				parts = append(parts, part{funcDeclName(fd), fd})
			}
			for _, p := range parts {
				key := file + " " + p.name
				ast.Inspect(p.node, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "RemoveAll" {
						found = append(found, treeDeleteFound{file, p.name, fset.Position(sel.Pos()).Line})
					}
					// A guard counts only as a call. A bare reference such as
					// "_ = homeguard.WipesHomeDir" guards nothing.
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "homeguard" {
						if guards[key] == nil {
							guards[key] = map[string]bool{}
						}
						guards[key]["homeguard."+sel.Sel.Name] = true
					}
					return true
				})
			}
		}
	}
	return found, guards
}

// TestTreeDeletesAreAllowlisted fails on a RemoveAll site outside
// treeDeleteSites, on a count that changed, and on an entry with no site.
func TestTreeDeletesAreAllowlisted(t *testing.T) {
	found, _ := scanTreeDeletes(t, productionGoFiles(t))
	checkTreeDeleteSites(t, found, treeDeleteSites)
}

// TestTreeDeletesKeepTheirHomeGuard fails when a function that an entry names
// in its guards no longer calls its home guard, and on an entry with neither
// a guard nor a reason for no guard.
func TestTreeDeletesKeepTheirHomeGuard(t *testing.T) {
	_, guards := scanTreeDeletes(t, productionGoFiles(t))
	checkTreeDeleteGuards(t, guards, treeDeleteSites)
}

func checkTreeDeleteSites(t *testing.T, found []treeDeleteFound, sites []treeDeleteSite) {
	t.Helper()
	counts := map[string]int{}
	lines := map[string][]string{}
	for _, f := range found {
		k := f.file + " " + f.fn
		counts[k]++
		lines[k] = append(lines[k], fmt.Sprintf("%s:%d", f.file, f.line))
	}
	listed := map[string]bool{}
	for _, s := range sites {
		k := s.file + " " + s.fn
		listed[k] = true
		switch got := counts[k]; {
		case got == 0:
			t.Errorf("treeDeleteSites lists %s in %s, but it holds no RemoveAll now. Drop the entry", s.fn, s.file)
		case got != s.count:
			t.Errorf("%s in %s holds %d RemoveAll (%s), but treeDeleteSites says %d. A new tree delete owes its AGENTS.md Part A item and its home guard first",
				s.fn, s.file, got, strings.Join(lines[k], ", "), s.count)
		}
	}
	var keys []string
	for k := range counts {
		if !listed[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Errorf("new tree delete at %s is not in treeDeleteSites. Read AGENTS.md, Gotchas Part A (the delete paths). Then add an entry with its Part A item and its home guard",
			strings.Join(lines[k], ", "))
	}
}

func checkTreeDeleteGuards(t *testing.T, guards map[string]map[string]bool, sites []treeDeleteSite) {
	t.Helper()
	for _, s := range sites {
		if s.why == "" {
			t.Errorf("treeDeleteSites entry %s in %s names no AGENTS.md Part A item", s.fn, s.file)
		}
		if len(s.guards) == 0 && s.noGuard == "" {
			t.Errorf("treeDeleteSites entry %s in %s names no home guard and no reason for none", s.fn, s.file)
		}
		for _, g := range s.guards {
			if !guards[g.file+" "+g.fn][g.call] {
				t.Errorf("%s in %s no longer calls %s, the home guard of the tree delete in %s (%s)", g.fn, g.file, g.call, s.fn, s.why)
			}
		}
	}
}

// TestRemoveAllGuardNeedsACall checks that a bare reference to a homeguard
// function counts as no guard, and that a call counts.
func TestRemoveAllGuardNeedsACall(t *testing.T) {
	file := filepath.ToSlash(filepath.Join(t.TempDir(), "guard.go"))
	src := `package p

func unused(p string) { _ = homeguard.WipesHomeDir }

func called(p string) bool { return homeguard.WipesHomeDir(p) }
`
	if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	_, guards := scanTreeDeletes(t, []string{file})
	if guards[file+" unused"][wipesHome] {
		t.Errorf("a bare reference to %s counts as a guard call", wipesHome)
	}
	if !guards[file+" called"][wipesHome] {
		t.Errorf("a call of %s does not count as a guard call", wipesHome)
	}
}
