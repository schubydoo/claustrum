package main

import "github.com/schubydoo/claustrum/internal/shellenv"

// The names of internal/shellenv under the names that the code of package main
// uses.

func warmLoginPATHAtStart() { shellenv.WarmAtStart() }

func awaitLoginPATH(noShellAgent bool) { shellenv.AwaitLoginPATH(noShellAgent) }

func currentLoginPATH() string { return shellenv.CurrentLoginPATH() }

func addShellAgentSocket(env []string, disabled bool) []string {
	return shellenv.AddAgentSocket(env, disabled)
}
