package main

import (
	"os"
	"sync"
	"testing"
)

// Fixture templates. A fixture repository with a fixed shape is built once per
// test binary, and each test copies it. A copy costs no git call, and a git
// call is slow on Windows.
var (
	fixtureTplMu   sync.Mutex
	fixtureTplRoot string
	fixtureTpls    = map[string]string{}
)

// copyFixtureTemplate copies the template named key into dst, which must not
// exist or must be empty. The first call for key runs build on an empty
// directory to make the template. If git acts on an absolute path in the
// template, such as a remote URL or a gitdir file, the caller must rewrite
// that path in dst.
func copyFixtureTemplate(t *testing.T, key, dst string, build func(t *testing.T, dir string)) {
	t.Helper()
	tpl := fixtureTemplate(t, key, build)
	if err := os.CopyFS(dst, os.DirFS(tpl)); err != nil {
		t.Fatalf("copy fixture template %s: %v", key, err)
	}
}

func fixtureTemplate(t *testing.T, key string, build func(t *testing.T, dir string)) string {
	t.Helper()
	fixtureTplMu.Lock()
	defer fixtureTplMu.Unlock()
	if tpl, ok := fixtureTpls[key]; ok {
		return tpl
	}
	if fixtureTplRoot == "" {
		t.Fatal("no fixture template root: TestMain did not call makeFixtureTemplateRoot")
	}
	tpl, err := os.MkdirTemp(fixtureTplRoot, key+"-")
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tpl)
		}
	}()
	build(t, tpl)
	fixtureTpls[key] = tpl
	ok = true
	return tpl
}

// makeFixtureTemplateRoot makes the directory that holds the templates.
// TestMain calls it before any test runs, so a test that points TMPDIR at its
// own TempDir cannot move the templates into a directory that a test deletes.
func makeFixtureTemplateRoot() error {
	root, err := os.MkdirTemp("", "claustrum-fixtpl-")
	if err != nil {
		return err
	}
	fixtureTplRoot = root
	return nil
}

// removeFixtureTemplates deletes every template. TestMain calls it after the
// tests ran.
func removeFixtureTemplates() {
	fixtureTplMu.Lock()
	defer fixtureTplMu.Unlock()
	if fixtureTplRoot != "" {
		_ = os.RemoveAll(fixtureTplRoot)
	}
}
