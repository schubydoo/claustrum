package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
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
	// Program is the command of a spawn with a launcher, where Argv0 is the
	// launcher. 89cb6289 writes it after argv0 and before start (VM-measured on
	// Linux). claustrum's choice (not measured): a spawn without a launcher omits
	// it, so that record keeps its 19f30c46 bytes.
	Program string `json:"program,omitempty"`
	Start   string `json:"start"`
	At      int64  `json:"at"`
}

// childrenDirName is the registry folder inside the run dir. Every record call names
// its file as "children/<file>" below a root that is the run dir. The error texts
// then hold that relative name, as the measured log lines do (Linux rows SP07a to
// SP07c).
const childrenDirName = "children"

// errChildrenNotDir reports that <runDir>/children exists and is not a real folder: a
// regular file, a symlink, or another kind. The daemon then writes no record.
var errChildrenNotDir = errors.New("children is not a directory")

// childRecordName is the name of the record of pid, relative to the run dir.
func childRecordName(pid int) string {
	return childrenDirName + "/" + strconv.Itoa(pid) + ".json"
}

// childRecordTempName is the temp name of the record of pid, relative to the run dir:
// "children/.rec-<pid>.json.<12 chars>". The shape is measured on Linux (row SP06).
// The source of the 12 chars is NOT measured. claustrum uses the nanosecond clock in
// base 36, which gives 12 chars at the present date.
// A var so a test can read the name.
var childRecordTempName = func(pid int) string {
	return childrenDirName + "/.rec-" + strconv.Itoa(pid) + ".json." + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// writeChildRecordIn marshals rec and writes it to children/<rec.Pid>.json below
// root, which is the run dir. It makes the children folder 0700, writes a temp file,
// then renames it into place, so a reader never sees a half-written record.
//
// Every step goes through the os.Root, so no step leaves the run dir. A children
// entry that is not a real folder gets no record: the Lstat does not follow a
// symlink, and the call returns errChildrenNotDir (Linux rows SP07a and SP07b). The
// os.Root also holds the run dir itself, so a renamed run dir gets the record under
// its new name (Linux row SP08).
//
// Uses encoding/json.Marshal, whose HTML escaping is inherited wire behaviour (see
// docs/ARCHITECTURE.md), so the observable record bytes match 19f30c46's.
func writeChildRecordIn(root *os.Root, rec childRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := root.Mkdir(childrenDirName, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	// The entry exists. It is a record folder only when it is a real folder itself.
	if fi, err := root.Lstat(childrenDirName); err != nil || !fi.IsDir() {
		return errChildrenNotDir
	}
	tmp := childRecordTempName(rec.Pid)
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, childRecordName(rec.Pid))
	}
	if err != nil {
		_ = root.Remove(tmp) // do not leave a stray temp behind
		return err
	}
	return nil
}

// removeChildRecordIn removes children/<pid>.json below root, which is the run dir. It
// is one plain remove of one name, never recursive, and the os.Root keeps it inside the
// run dir. A record that does not exist is not an error. As 89cb6289 does, the remove
// goes through a children symlink that stays inside the run dir (Linux rows SLa, SLb,
// macOS KDa, KDb). A symlink that leads out is refused (row SP07b).
func removeChildRecordIn(root *os.Root, pid int) error {
	err := root.Remove(childRecordName(pid))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
