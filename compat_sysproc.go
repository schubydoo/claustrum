package main

import (
	"os"
	"syscall"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The names of internal/sysproc under the names that the code of package main
// uses.

type cappedText = sysproc.CappedText

func newSysProcAttr() *syscall.SysProcAttr { return sysproc.NewSysProcAttr() }

func reapProcessGroup(proc *os.Process) { sysproc.ReapProcessGroup(proc) }

func exitSignalName(waitErr error) string { return sysproc.ExitSignalName(waitErr) }

func closeAll(fs ...*os.File) { sysproc.CloseAll(fs...) }

func removeEnvKey(env []string, k string) []string { return sysproc.RemoveEnvKey(env, k) }

func replaceOrAppendEnv(env []string, key, val string) []string {
	return sysproc.ReplaceOrAppendEnv(env, key, val)
}
