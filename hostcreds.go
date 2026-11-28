package main

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/supervisor"
)

// hostCredsValue reads the hostCreds member v of process.spawn. set is false when it
// is absent. ok is false for a value that is no object with an env object of
// strings: 13047a51 answers null, a text, a list and an env value that is a number
// with Invalid params (measured on Linux, macOS and Windows). An env that is a text
// or a list and an env value of null get it too (measured on Linux and macOS). Other
// members, such as unless, pass and change nothing
// (measured on Linux and macOS). An env of null counts as an empty env (claustrum's
// own, not measured).
func hostCredsValue(v json.RawMessage) (env map[string]string, set, ok bool) {
	s := bytes.TrimSpace(v)
	if len(s) == 0 {
		return nil, false, true
	}
	var o struct {
		Env map[string]*string `json:"env"`
	}
	if string(s) == "null" || json.Unmarshal(s, &o) != nil {
		return nil, true, false
	}
	env = make(map[string]string, len(o.Env))
	for k, val := range o.Env {
		if val == nil {
			return nil, true, false
		}
		env[k] = *val
	}
	return env, true, true
}

// hostCredsLimit is the most bytes that the names and values of a hostCreds env can
// hold. 13047a51 refuses a value of 70000 bytes and passes 2000 short names (measured
// on Linux). The exact bound, and the count of a name and its value as their two
// lengths, are claustrum's own, not measured.
const hostCredsLimit = 32 * 1024

// The two env names that a hostCreds spawn sets to 1 in its own env (13047a51,
// measured on Linux with both missing and with one of them 0).
const (
	hostManagedName = "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"
	hostRefreshName = "CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH"
)

// hostCredsText is the refusal text of a hostCreds spawn on Linux, or "" when the
// spawn passes. Each text is measured on Linux against 13047a51. A name with a space,
// a name that starts with a digit, a NUL in a value, and the names
// CLAUDE_CODE_HOST_CREDS_FILE and LD_PRELOAD pass (measured). The combine test comes
// before the Claude Code test: a gitRead spawn of git gets the combine text (measured).
// The rest of the order is claustrum's own, not measured. The Claude Code test is
// claudeCodeShape: a command named ccd-cli passes and another program is refused
// (measured). Other shapes are claustrum's own.
func hostCredsText(p *spawnParams, env map[string]string) string {
	if len(env) == 0 {
		return "hostCreds refused: its env is empty"
	}
	size := 0
	for k, v := range env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return "hostCreds refused: a name is empty or holds = or NUL"
		}
		size += len(k) + len(v)
	}
	if size > hostCredsLimit {
		return "hostCreds refused: its names and values come to over 32 KiB"
	}
	if ptyObject(p.Pty) || p.LinkBound != nil || p.GitRead || p.Launcher != nil {
		return "hostCreds refused: it came with pty, linkBound, gitRead or launcher"
	}
	for k := range env {
		if _, dup := p.Env[k]; dup {
			return "hostCreds refused: a name is also in the spawn's env"
		}
	}
	if !claudeCodeShape(p.Command, p.Args, p.Launcher != nil) {
		return "hostCreds refused: the program is not Claude Code, which alone reads it"
	}
	if p.Env[hostManagedName] != "1" || p.Env[hostRefreshName] != "1" {
		return "hostCreds refused: the spawn's env does not set CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST and CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH to 1"
	}
	return ""
}

// hostCredsFeature is the feature name of the pipe (hostCredsFeatures).
const hostCredsFeature = "process.spawn.host_creds_pipe"

// hostCredsUnsupportedText is the refusal of any hostCreds of the right form on macOS
// and Windows (13047a51, measured on macOS and Windows). No child starts (measured
// on macOS).
const hostCredsUnsupportedText = "hostCreds is not supported on this host"

// hostCredsRefusal is the refusal of a spawn with a hostCreds env, or nil. A pty
// object with no valid key keeps its own text (13047a51, measured on Linux and
// Windows). Each refusal is -32602 with the reason other and logs its failed-start line
// (measured on Linux, and on macOS and Windows for hostCredsUnsupportedText). On
// Windows the test comes before the -32004 launcher refusal (measured). Its place after
// the id, launcher shape, command, unattendedSeconds and linkBound tests is claustrum's
// own, not measured. So is its place on Windows after the keyed pty refusal, and on
// Linux and macOS before the launcher refusal. It comes before the gitRead combine
// test (measured on Linux).
func hostCredsRefusal(req *request, p *spawnParams, env map[string]string) *rpc.Response {
	if bad := ptyKeyRefusal(req, p.Pty); bad != nil {
		return bad
	}
	text := hostCredsUnsupportedText
	if hostCredsPipe {
		text = hostCredsText(p, env)
	}
	if text == "" {
		return nil
	}
	logging.Errorf("[process.Manager] Failed to start process %s: %s", supervisor.LogID(p.ID), text)
	return rpc.Ptr(rpc.ErrResultData(req.ID, rpc.CodeInvalidParam, text, supervisor.SpawnReason{Reason: "other"}))
}
