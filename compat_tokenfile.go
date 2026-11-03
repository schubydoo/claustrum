package main

import (
	"os"

	"github.com/schubydoo/claustrum/internal/tokenfile"
)

// The names of internal/tokenfile under the names that the code of package main
// uses.

const persistedTokenName = tokenfile.Name

type tokenTempFile = tokenfile.TempFile

func tokenFileDir(socket string) string { return tokenfile.Dir(socket) }

func writeFileViaTemp(create func(string) (tokenTempFile, error), dir, dest, content string) error {
	return tokenfile.WriteViaTemp(create, dir, dest, content)
}

func saveTokenFile(socket, token string) os.FileInfo { return tokenfile.Save(socket, token) }

func statOpenFile(path string) (os.FileInfo, error) { return tokenfile.StatOpen(path) }

func dropOwnTokenFile(socket string, owned os.FileInfo) { tokenfile.DropOwn(socket, owned) }

func removeIfStillOurs(path string, owned os.FileInfo) error {
	return tokenfile.RemoveIfStillOurs(path, owned)
}
