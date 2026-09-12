package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// childRecord is the per-spawn orphan-registry record the daemon writes to
// <runDir>/children/<pid>.json (reference build 19f30c46). A later daemon's
// orphan-reap step reads these to decide whether a leftover child of a since-exited
// daemon is an orphan to end. The field ORDER and the string-vs-number typing are the
// on-disk contract — measured byte-for-byte against 19f30c46: the fields appear in a
// fixed, non-key-sorted order, with daemonStart and start as clock-tick STRINGS and
// pid, daemonPid and at as numbers. It is an ordered struct, never a map, for the same
// reason the wire results are.
type childRecord struct {
	Pid         int    `json:"pid"`
	Node        string `json:"node"`
	Host        string `json:"host"`
	Instance    string `json:"instance"`
	DaemonPid   int    `json:"daemonPid"`
	DaemonStart string `json:"daemonStart"`
	Argv0       string `json:"argv0"`
	Start       string `json:"start"`
	At          int64  `json:"at"`
}

// writeChildRecord marshals rec and writes it atomically to
// <runDir>/children/<rec.Pid>.json — the on-disk path measured against 19f30c46. It
// makes the children dir 0700, writes a temp file, then renames it into place so a
// reader never sees a half-written record. Pure filesystem work and so cross-platform;
// the linux caller supplies the record. Uses encoding/json.Marshal, whose HTML
// escaping is inherited wire behaviour (see docs/ARCHITECTURE.md), so the observable
// record bytes match 19f30c46's.
func writeChildRecord(runDir string, rec childRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	dir := filepath.Join(runDir, "children")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	final := filepath.Join(dir, strconv.Itoa(rec.Pid)+".json")
	tmp := filepath.Join(dir, strconv.Itoa(rec.Pid)+"."+strconv.FormatInt(time.Now().UnixNano(), 10)+".json")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp) // do not leave a stray temp behind on a failed rename
		return err
	}
	return nil
}
