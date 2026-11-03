package main

import "os"

// fixtureTplRoot is the folder that TestMain makes before any test runs. The
// claustrum binary of buildClaustrum goes below it, so a test that points
// TMPDIR at its own TempDir cannot move it into a directory that a test
// deletes.
var fixtureTplRoot string

// makeFixtureTemplateRoot makes the folder of fixtureTplRoot.
func makeFixtureTemplateRoot() error {
	root, err := os.MkdirTemp("", "claustrum-fixtpl-")
	if err != nil {
		return err
	}
	fixtureTplRoot = root
	return nil
}

// removeFixtureTemplates deletes that folder. TestMain calls it after the tests
// ran.
func removeFixtureTemplates() {
	if fixtureTplRoot != "" {
		_ = os.RemoveAll(fixtureTplRoot)
	}
}
