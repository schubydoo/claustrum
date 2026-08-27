//go:build windows

package main

// oDirectoryFlag is 0 on Windows, which has no O_DIRECTORY: os.OpenFile behaves
// like os.Open, so a non-directory opens and fails at the later readdir instead.
// The non-directory error wording is not pinned on Windows (the tests skip it),
// and the reference is a single Go build that has no O_DIRECTORY there either.
const oDirectoryFlag = 0
