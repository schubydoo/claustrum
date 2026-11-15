//go:build !windows

package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/rpc"
	"golang.org/x/sys/unix"
)

// roomFake is a fake free space and a fake group kill for the watch of the
// private LFS store. The private folder gets start free at the first read and
// then low. Every other path gets 1 TiB, or low with otherDisk. After calls
// reads of the private folder, or at the kill, the stand-in download ends
// (remote/end). The kill reaches no process: it records the pid and its group.
type roomFake struct {
	remote, oid string
	start, low  uint64
	otherDisk   bool
	calls       int

	mu         sync.Mutex
	dir        string
	reads      int
	killedPid  int
	killedPgid int
	kills      int
	cmdline    string
}

func (r *roomFake) install(t *testing.T) {
	t.Helper()
	prevFree, prevReap, prevPoll := storeFreeBytes, reapCheckoutGroup, lfsRoomPoll
	storeFreeBytes = r.free
	reapCheckoutGroup = r.kill
	lfsRoomPoll = 10 * time.Millisecond
	t.Cleanup(func() { storeFreeBytes, reapCheckoutGroup, lfsRoomPoll = prevFree, prevReap, prevPoll })
}

func (r *roomFake) free(p string) (uint64, bool) {
	if !strings.HasPrefix(filepath.Base(p), checkoutDirPrefix) {
		if r.otherDisk {
			return r.low, true
		}
		return 1 << 40, true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dir = p
	r.reads++
	if r.reads == 1 || r.otherDisk {
		if r.reads > r.calls {
			r.endDownload()
		}
		return r.start, true
	}
	if r.reads == 2 {
		// The files that git-lfs fetched: a part of 1.5 MiB and a whole object.
		_ = os.WriteFile(filepath.Join(p, "lfs", "incomplete", "part"), make([]byte, 3<<19), 0o644)
		obj := storeObjectPath(filepath.Join(p, "lfs"), r.oid)
		_ = os.MkdirAll(filepath.Dir(obj), 0o755)
		_ = os.WriteFile(obj, []byte("alpha-object"), 0o644)
	}
	if r.reads > r.calls {
		r.endDownload()
	}
	return r.low, true
}

func (r *roomFake) kill(p *os.Process) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kills++
	r.killedPid = p.Pid
	r.killedPgid, _ = unix.Getpgid(p.Pid)
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(p.Pid) + "/cmdline"); err == nil {
		r.cmdline = strings.ReplaceAll(string(b), "\x00", " ")
	}
	r.endDownload()
}

func (r *roomFake) endDownload() {
	_ = os.WriteFile(filepath.Join(r.remote, "end"), nil, 0o644)
}

// dispatchBeforeAfterWrite runs one git request as gitDispatch does, and calls
// at between the reply and its AfterWrite.
func dispatchBeforeAfterWrite(t *testing.T, d *Deps, line string, at func()) string {
	t.Helper()
	var req rpc.Request
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		t.Fatal(err)
	}
	resp := Handle(d, &req)
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	at()
	if resp.AfterWrite != nil {
		resp.AfterWrite()
	}
	return string(b)
}

// TestLFSRoomStop: while git-lfs downloads into the private store (an
// `incomplete` folder there), a free space under a tenth of the free space at
// the start stops the checkout. The group of the read-tree call gets the kill,
// the rollback runs, the private folder is gone at the reply and nothing is
// kept. The reply is worktree_lfs_no_space (4c111e86, measured on Linux). The
// AfterWrite remove does not run a second time.
func TestLFSRoomStop(t *testing.T) {
	const mib = 1 << 20
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
	mustWrite(t, filepath.Join(f.remote, "hang"), "")
	mustWrite(t, filepath.Join(f.remote, "incomplete"), "")
	r := &roomFake{remote: f.remote, oid: f.oids["a.bin"], start: 1000 * mib, low: 99 * mib, calls: 1000}
	r.install(t)
	params := map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": f.leaf}
	var atReply bool
	raw := dispatchBeforeAfterWrite(t, f.d, rpcLine(t, "git.worktree_create", params), func() {
		atReply = exists(r.dir)
		// A new folder at the same name stays after AfterWrite.
		if !atReply {
			if err := os.Mkdir(r.dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	})
	text := "Claude stopped the checkout when only 99 MiB was left free on the disk that holds " + f.run +
		" on the remote machine. The worktree and the 2 MiB of Git LFS files already fetched into that folder were removed." +
		" Free up space on that disk, or turn off Worktree for this session, then try again." +
		" Claude keeps a checkout's Git LFS files in that folder until the checkout ends, so they need room on that disk."
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonQuoted(t, text) + `,"errorCode":"worktree_lfs_no_space"}}`
	if raw != want {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if atReply {
		t.Errorf("the private folder %s is there at the reply, want it gone", r.dir)
	}
	if !atReply && !exists(r.dir) {
		t.Errorf("the new folder %s went at AfterWrite, want the remove to run once", r.dir)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.kills != 1 || r.killedPid < 2 || r.killedPgid != r.killedPid || r.killedPgid == unix.Getpgrp() {
		t.Errorf("kills = %d of pid %d in group %d, want one of the leader of the group of the read-tree call", r.kills, r.killedPid, r.killedPgid)
	}
	if runtime.GOOS == "linux" && !strings.Contains(r.cmdline, "read-tree") {
		t.Errorf("the killed process runs %q, want the read-tree call", r.cmdline)
	}
	if exists(storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids["a.bin"])) {
		t.Error("the fetched object reached the store of the repository, want nothing kept")
	}
	f.wantGone(t)
}

// TestLFSRoomNoStop: no stop at a free space of a tenth or more, with no
// `incomplete` folder (seeded objects, measured on Linux), or when only another
// disk fills (a HOME on another disk, measured on Linux). The poll reads no free
// space with no `incomplete` folder. The exact bound of a tenth is claustrum's
// own, not measured.
func TestLFSRoomNoStop(t *testing.T) {
	const mib = 1 << 20
	for _, row := range []struct {
		name       string
		incomplete bool
		low        uint64
		otherDisk  bool
		reads      int
	}{
		{"at a tenth", true, 100 * mib, false, 6},
		{"no incomplete folder", false, 1, false, 1},
		{"another disk fills", true, 1, true, 6},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
			mustWrite(t, filepath.Join(f.remote, "hang"), "")
			if row.incomplete {
				mustWrite(t, filepath.Join(f.remote, "incomplete"), "")
			}
			r := &roomFake{remote: f.remote, oid: f.oids["a.bin"], start: 1000 * mib, low: row.low, otherDisk: row.otherDisk, calls: 5}
			r.install(t)
			if !row.incomplete {
				// The poll reads nothing, so the download ends after 50 polls.
				go func() {
					time.Sleep(50 * lfsRoomPoll)
					r.endDownload()
				}()
			}
			raw, _ := f.create(t, nil)
			if !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
				t.Errorf("git.worktree_create = %s, want worktree_add_failed", raw)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.kills != 0 {
				t.Errorf("kills = %d, want none", r.kills)
			}
			if (row.reads == 1 && r.reads != 1) || r.reads < row.reads {
				t.Errorf("reads of the free space of the private folder = %d, want %d", r.reads, row.reads)
			}
			f.wantGone(t)
		})
	}
}
