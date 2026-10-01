package main

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// The daemon's own GIT_* variables on the git calls of the git methods. The rules
// are in docs/PROTOCOL.md, "The daemon's own git environment".
//
//   - A repository call drops GIT_CONFIG and GIT_CONFIG_PARAMETERS. The names match
//     case-sensitively.
//   - The daemon's GIT_CONFIG_COUNT must parse, and each of its pairs must be set.
//   - A hardened call gets GIT_CONFIG_COUNT, the inherited pairs and the hook pins
//     after the profile and the GIT_COMMON_DIR pin. Every other
//     GIT_CONFIG_KEY_<digits> and GIT_CONFIG_VALUE_<digits> name is removed.
//   - The light profile keeps only the https and ssh entries of the daemon's
//     GIT_ALLOW_PROTOCOL.
//
// Three calls get the daemon's environment with only their own additions: the
// excludes read, the --attr-source version probe and the plain `git version` of
// worktreeinclude.go.
//
// GIT_CONFIG_COUNT and its pairs are read by their exact names from the environment
// list, because os.LookupEnv ignores case on Windows. GIT_ALLOW_PROTOCOL is read
// with os.LookupEnv.

// hooksPinPrefix starts every hooks refusal.
const hooksPinPrefix = "config-defined hooks could not be pinned off; git not run: "

const (
	configCountName  = "GIT_CONFIG_COUNT"
	configKeyPrefix  = "GIT_CONFIG_KEY_"
	configValPrefix  = "GIT_CONFIG_VALUE_"
	allowProtocolVar = "GIT_ALLOW_PROTOCOL"
)

// envName is the name part of a KEY=VALUE entry.
func envName(kv string) string {
	name, _, _ := strings.Cut(kv, "=")
	return name
}

// lookupExact returns the value of the first entry of env named exactly name.
func lookupExact(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == name {
			return v, true
		}
	}
	return "", false
}

// dropConfigOverrides removes GIT_CONFIG and GIT_CONFIG_PARAMETERS from env. The
// names match exactly, also on Windows.
func dropConfigOverrides(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if n := envName(kv); n != "GIT_CONFIG" && n != "GIT_CONFIG_PARAMETERS" {
			out = append(out, kv)
		}
	}
	return out
}

// isConfigPairName reports whether name is GIT_CONFIG_KEY_ or GIT_CONFIG_VALUE_
// followed by one or more digits. Leading zeros count, so KEY_01 is a pair name here.
func isConfigPairName(name string) bool {
	rest, ok := strings.CutPrefix(name, configKeyPrefix)
	if !ok {
		if rest, ok = strings.CutPrefix(name, configValPrefix); !ok {
			return false
		}
	}
	return rest != "" && strings.Trim(rest, "0123456789") == ""
}

// withoutConfigCountSet is env for a call that carries the hook pins: GIT_CONFIG_COUNT
// and every GIT_CONFIG_KEY_<digits> and GIT_CONFIG_VALUE_<digits> are removed.
// configPinEnv puts the kept pairs back after the profile and the GIT_COMMON_DIR pin.
func withoutConfigCountSet(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if name := envName(kv); isConfigPairName(name) || name == configCountName {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// configPinEnv follows the profile and the GIT_COMMON_DIR pin in a hardened call's
// environment. It is GIT_CONFIG_COUNT, then the count inherited pairs of env in index
// order, read by their canonical names. Then come the hook pins: hook.enabled=false
// and an empty hook.event, then hook.<name>.enabled=false and an empty
// hook.<name>.event for each name of hooks, in order. The count covers every pair.
// 89cb6289 has that order on Linux, macOS and Windows VMs (rows L04 to L07).
func configPinEnv(env []string, count int, hooks []string) []string {
	out := []string{configCountName + "=" + strconv.Itoa(count+2+2*len(hooks))}
	for i := range count {
		n := strconv.Itoa(i)
		k, _ := lookupExact(env, configKeyPrefix+n)
		v, _ := lookupExact(env, configValPrefix+n)
		out = append(out, configKeyPrefix+n+"="+k, configValPrefix+n+"="+v)
	}
	pin := func(key, value string) {
		n := strconv.Itoa(count)
		out = append(out, configKeyPrefix+n+"="+key, configValPrefix+n+"="+value)
		count++
	}
	pin("hook.enabled", "false")
	pin("hook.event", "")
	for _, h := range hooks {
		pin("hook."+h+".enabled", "false")
		pin("hook."+h+".event", "")
	}
	return out
}

// countFromText reads a GIT_CONFIG_COUNT value. The empty value is 0. Else any
// leading space, \t, \n, \v, \f and \r bytes are skipped, then one optional "+",
// then one or more digits and the end. A value above the int range does not parse.
func countFromText(v string) (int, bool) {
	if v == "" {
		return 0, true
	}
	s := strings.TrimPrefix(strings.TrimLeft(v, " \t\n\v\f\r"), "+")
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// inheritedConfigProblem checks the GIT_CONFIG_COUNT and its pairs in env. It
// returns the count, or the refusal text.
func inheritedConfigProblem(env []string) (int, string) {
	v, set := lookupExact(env, configCountName)
	if !set {
		return 0, ""
	}
	n, ok := countFromText(v)
	if !ok {
		return 0, fmt.Sprintf("inherited GIT_CONFIG_COUNT %q is not a count", v)
	}
	for i := range n {
		_, keySet := lookupExact(env, configKeyPrefix+strconv.Itoa(i))
		_, valSet := lookupExact(env, configValPrefix+strconv.Itoa(i))
		if !keySet || !valSet {
			return 0, fmt.Sprintf("inherited GIT_CONFIG pair %d is incomplete", i)
		}
	}
	return n, ""
}

// daemonCountRefusal is the check of the daemon's own GIT_CONFIG_COUNT that the
// git methods make. It runs the excludes read first. It returns the whole refusal
// text and true when the count or one of its pairs is refused.
func daemonCountRefusal() (string, bool) {
	userExcludesFile()
	if _, text := inheritedConfigProblem(os.Environ()); text != "" {
		return hooksPinPrefix + text, true
	}
	return "", false
}

// lightAllowProtocol is the GIT_ALLOW_PROTOCOL value of the light profile. Unset in
// the daemon's environment, it is https:ssh. Else the daemon's list is split on ":",
// and each entry that is exactly https or ssh is kept once, in its order. When none
// is left, the value is denied_by_claude_ssh.
func lightAllowProtocol() string {
	v, set := os.LookupEnv(allowProtocolVar)
	if !set {
		return "https:ssh"
	}
	var kept []string
	for _, p := range strings.Split(v, ":") {
		if (p == "https" || p == "ssh") && !slices.Contains(kept, p) {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return "denied_by_claude_ssh"
	}
	return strings.Join(kept, ":")
}
