package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"

	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/supervisor"
)

// The host settings file sets the unattended limit of the daemon. 13047a51 reads it on
// Linux only, at the start (measured on Linux, and on macOS and Windows that it reads
// none). hostlimit_linux.go reads the file. This file judges its bytes and holds the texts.
// limitend.go ends the daemon at the limit.

// hostLimitFeature is the feature of a daemon that reads the host settings file.
const hostLimitFeature = "server.unattended_limit"

// The bounds of unattendedLimitMinutes. A value below the low bound counts as 15, one above
// the high bound as 43200 (13047a51, measured on Linux for 1, 14, 43201 and 10^12).
const (
	hostLimitLow  = 15
	hostLimitHigh = 43200
	// hostLimitMaxSize is the largest file that is read. 65536 bytes pass and 65537 do not
	// (measured on Linux).
	hostLimitMaxSize = 64 << 10
)

// The settingsFault values of a refused Claude Code spawn (measured on Linux).
const (
	hostFaultValue      = "value"
	hostFaultSyntax     = "syntax"
	hostFaultUnreadable = "unreadable"
	hostFaultWritable   = "writable"
	hostFaultOwner      = "owner"
)

// The detail texts of a fault (measured on Linux).
const (
	hostDetailValue    = "unattendedLimitMinutes must be a whole number of minutes, 1 or more, with no quotes. To set no limit, remove it."
	hostDetailSyntax   = `It must hold one JSON object, such as {"unattendedLimitMinutes": 60}.`
	hostDetailTooLarge = "It can't be read: it is larger than 64 KiB."
	hostDetailNotFile  = "It can't be read: it is not a regular file."
	hostDetailNoRead   = "The account that signs in can't read it. Give it mode 0644."
)

// hostLimit is what the start read of the host settings file found. minutes is 0 when no
// limit is set. fault is set for a file that cannot be used, and then minutes is 0.
type hostLimit struct {
	path    string
	minutes int
	fault   string
	detail  string
	// asked is the value of the file as written, when it lay outside the bounds.
	asked string
	// unknown counts the other keys of the object, and firstUnknown is the first of them.
	unknown      int
	firstUnknown string
}

// hostFileFault is a file rule that the file or its folder breaks (hostlimit_linux.go).
type hostFileFault struct{ fault, detail string }

func (e *hostFileFault) Error() string { return e.detail }

// errHostLimitTooLarge is the read error of a file above hostLimitMaxSize, and
// errHostLimitNotFile the one of a path that is not a regular file.
var (
	errHostLimitTooLarge = errors.New("host settings file too large")
	errHostLimitNotFile  = errors.New("host settings file is not a regular file")
)

// readHostLimitAtStart reads the host settings file once and logs its one line. It reads
// nothing when hostLimitPath is empty, as off Linux.
func readHostLimitAtStart() hostLimit {
	if hostLimitPath == "" {
		return hostLimit{}
	}
	h := readHostLimit(hostLimitPath)
	h.logStart()
	return h
}

// readHostLimit reads the file at path and judges it. No folder sets no limit (measured on
// Linux). A folder with no file sets none either (claustrum's own, not measured). A link
// is followed (measured on Linux). A folder, and any other kind that is not a regular
// file, cannot be used (measured on Linux for a folder).
// An open that is refused for its permission cannot be used (measured on Linux for a root
// file of mode 0600), unless the file breaks the owner or a mode rule (unreadableFault). Any other read error sets no limit and refuses no spawn, with one
// WARN line (claustrum's own, not measured). A broken owner or mode rule cannot be used
// (hostFileRules).
func readHostLimit(path string) hostLimit {
	h := hostLimit{path: path}
	b, err := readHostLimitFile(path)
	switch {
	case err == nil:
		return judgeHostLimit(path, b)
	case errors.Is(err, fs.ErrNotExist):
		return h
	case errors.Is(err, fs.ErrPermission):
		return h.withFault(hostFaultUnreadable, hostDetailNoRead)
	case errors.Is(err, errHostLimitTooLarge):
		return h.withFault(hostFaultUnreadable, hostDetailTooLarge)
	case errors.Is(err, errHostLimitNotFile):
		return h.withFault(hostFaultUnreadable, hostDetailNotFile)
	}
	var ff *hostFileFault
	if errors.As(err, &ff) {
		return h.withFault(ff.fault, ff.detail)
	}
	logging.Warnf("[Server] cannot read %s (%v): no unattended limit is set", path, err)
	return h
}

// judgeHostLimit gives the limit of the bytes b of the file at path. A leading UTF-8 byte
// order mark and a newline after the object pass (measured on Linux). The file must hold
// one JSON object: an empty file, an array, a number, text after the object and two objects
// fail (measured on Linux). A missing key sets no limit. Its value must be a JSON integer
// of 1 or more: 0, -1, 1.5, "60", true and null fail (measured on Linux). Other forms such
// as 1e3, and the file null, are claustrum's own, not measured. A key other than
// unattendedLimitMinutes sets nothing, and the start line counts the other keys and names
// the first (measured on Linux for 2 keys and for 1). Only the keys of the top object
// count (measured on Linux). The first is the first in the file, and a key twice counts
// once (claustrum's own, not measured).
func judgeHostLimit(path string, b []byte) hostLimit {
	h := hostLimit{path: path}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	dec := json.NewDecoder(bytes.NewReader(b))
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return h.withFault(hostFaultSyntax, hostDetailSyntax)
	}
	var more json.RawMessage
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		return h.withFault(hostFaultSyntax, hostDetailSyntax)
	}
	h.unknown = len(obj)
	if _, ok := obj["unattendedLimitMinutes"]; ok {
		h.unknown--
	}
	if h.unknown > 0 {
		h.firstUnknown = firstOtherKey(b)
	}
	v, ok := obj["unattendedLimitMinutes"]
	if !ok {
		return h
	}
	digits := string(bytes.TrimSpace(v))
	if !allDigits(digits) || digits[0] == '0' {
		return h.withFault(hostFaultValue, hostDetailValue)
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		// Past the range of an int: it counts as the high bound (claustrum's own).
		n = hostLimitHigh + 1
	}
	switch {
	case n < hostLimitLow:
		h.minutes, h.asked = hostLimitLow, digits
	case n > hostLimitHigh:
		h.minutes, h.asked = hostLimitHigh, digits
	default:
		h.minutes = n
	}
	return h
}

// firstOtherKey gives the first key of the object b other than unattendedLimitMinutes, in
// the order of the file. b is one JSON object that decoded.
func firstOtherKey(b []byte) string {
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil {
		return ""
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return ""
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return ""
		}
		if k, _ := t.(string); k != "unattendedLimitMinutes" {
			return k
		}
	}
	return ""
}

// allDigits is true for a non-empty s of ASCII digits only.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func (h hostLimit) withFault(fault, detail string) hostLimit {
	h.minutes, h.fault, h.detail, h.unknown = 0, fault, detail, 0
	return h
}

// unknownTail is the tail of the start line for the other keys, or "". With the clamp
// tail it comes last, and with no limit it follows the no-limit line (claustrum's own,
// not measured). The quotes of the key are those of Go (claustrum's own beyond a plain
// name).
func (h hostLimit) unknownTail() string {
	if h.unknown == 0 {
		return ""
	}
	return fmt.Sprintf("; %d key(s) are unknown to this version of the helper and ignored, the first of them %s (a misspelt key sets nothing)", h.unknown, strconv.Quote(h.firstUnknown))
}

// logStart logs the one start line of the read (13047a51, measured on Linux). It comes
// right after the open-files line and before the listening line. The log levels are
// claustrum's own.
func (h hostLimit) logStart() {
	switch {
	case h.fault != "":
		logging.Warnf("[Server] %s can't be used, so no Claude Code is started until it is fixed. %s No unattended limit applies meanwhile.", h.path, h.detail)
	case h.minutes == 0:
		logging.Infof("[Server] No unattended limit is set in %s%s", h.path, h.unknownTail())
	case h.asked != "":
		logging.Infof("[Server] %s sets an unattended limit of %d minutes: once nobody has been connected for that long, this helper ends itself and what it started; unattendedLimitMinutes is %s, which counts as %d (the bounds are %d and %d)%s", h.path, h.minutes, h.asked, h.minutes, hostLimitLow, hostLimitHigh, h.unknownTail())
	default:
		logging.Infof("[Server] %s sets an unattended limit of %d minutes: once nobody has been connected for that long, this helper ends itself and what it started%s", h.path, h.minutes, h.unknownTail())
	}
}

// spawnRefusal is the reply to a spawn p of a Claude Code shape while the file cannot be
// used, and nil otherwise. 13047a51 answers -32010 with the reason host_settings and a
// settingsFault member, and logs its failed-start line (measured on Linux). A plain spawn
// starts (measured on Linux).
func (h hostLimit) spawnRefusal(id interface{}, p *spawnParams) *rpc.Response {
	if h.fault == "" || !claudeCodeShape(p.Command, p.Args, p.Launcher != nil) {
		return nil
	}
	procID := p.ID
	msg := "Claude Code was not started: this host's settings file for Claude Desktop, " + h.path + ", can't be used. " + h.detail + " Only the host's administrator can change it."
	logging.Errorf("[process.Manager] Failed to start process %s: %s", supervisor.LogID(procID), msg)
	return rpc.Ptr(rpc.ErrResultData(id, rpc.CodeHostSettings, msg, supervisor.SpawnReason{Reason: "host_settings", SettingsFault: h.fault}))
}
