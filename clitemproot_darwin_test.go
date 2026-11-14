package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/schubydoo/claustrum/internal/procinfo"
)

// On macOS only CLAUDE_CODE_TMPDIR names the temp dir (c9125eb8 and 4c111e86,
// measured on macOS for TMPDIR). The folder has mode 0500 and holds t of mode 0700,
// and the child gets t (4c111e86, measured on macOS). TMP and TEMP are claustrum's
// own.
func TestCLITempRootOnMacOSTakesOnlyClaudeCodeTmpdir(t *testing.T) {
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Run(key, func(t *testing.T) {
			var rel []func()
			cleanTempRoots(t, &rel)
			x, runDir := tempRootDir(t, true)
			in := []string{key + "=" + x}
			env, res, release, _ := privateTempRoot(in, runDir)
			rel = append(rel, release)
			if res != nil || !slices.Equal(env, in) || len(ccdFolders(t, x)) != 0 {
				t.Errorf("answer %+v, env %q, want none and the env as it was", res, env)
			}
		})
	}
	t.Run("CLAUDE_CODE_TMPDIR", func(t *testing.T) {
		var rel []func()
		cleanTempRoots(t, &rel)
		x, runDir := tempRootDir(t, true)
		env, res, release, _ := privateTempRoot([]string{"TMPDIR=" + x, "CLAUDE_CODE_TMPDIR=" + x}, runDir)
		rel = append(rel, release)
		if res == nil || res.Refused != unfitKind || !tempRootNameRE.MatchString(res.Name) || res.WhyNot != "" {
			t.Fatalf("answer %+v, want refused %s and a name of 10 hex", res, unfitKind)
		}
		if got := ccdFolders(t, x); !slices.Equal(got, []string{res.Name}) {
			t.Errorf("folders %v, want %s", got, res.Name)
		}
		if v := tempEnvValue(env, "CLAUDE_CODE_TMPDIR"); v != filepath.Join(x, res.Name, "t") {
			t.Errorf("CLAUDE_CODE_TMPDIR %q, want %q", v, filepath.Join(x, res.Name, "t"))
		}
		if v := tempEnvValue(env, "TMPDIR"); v != x {
			t.Errorf("TMPDIR %q, want %q", v, x)
		}
		for _, c := range []struct {
			path string
			mode os.FileMode
		}{{filepath.Join(x, res.Name), 0o500}, {filepath.Join(x, res.Name, "t"), 0o700}} {
			if fi, err := os.Lstat(c.path); err != nil || !fi.IsDir() || fi.Mode().Perm() != c.mode {
				t.Errorf("%s: %v %v, want a folder of mode %o", c.path, fi, err, c.mode)
			}
		}
	})
}

// The registry name on macOS takes the boot session id and an empty pid namespace
// (claustrum's own, not measured).
func TestCLITempRootRegistryNameOnMacOS(t *testing.T) {
	boot := procinfo.NodeID()
	if boot == "" {
		t.Skip("no kern.bootsessionuuid")
	}
	if got, want := tempRootRegistryName("/tmp/r/X"), "cli-temp-root-"+sha256Hex8(boot+"/"+string(rune(0))+"/tmp/r/X"); got != want {
		t.Errorf("name %q, want %q", got, want)
	}
}

// The entry test reads the device of a macOS stat: an entry on another device is on
// another file system.
func TestCLITempRootEntryKindsOnMacOS(t *testing.T) {
	uid := 501
	for _, c := range []struct {
		dev  int32
		want string
	}{{7, ""}, {8, "on another file system"}} {
		st := &unix.Stat_t{Mode: unix.S_IFDIR | 0o700, Uid: uint32(uid), Dev: c.dev}
		if got := tempEntryUnfit(st, 7, uid); got != c.want {
			t.Errorf("dev %d: %q, want %q", c.dev, got, c.want)
		}
	}
}

// A spawn with cliTempRoot and cliTempRootEnd is ended when its folder is lost, and
// its exit frame carries the killedBy "temp_root" (4c111e86, measured on macOS). The
// child is the helper, which ends on the SIGTERM.
func TestSpawnCLITempRootEndOnALossOnMacOS(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	x, _ := tempRootDir(t, true)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	exe, env := helperCommand(t, "cat")
	env["CLAUDE_CODE_TMPDIR"] = x
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": "te", "command": exe, "env": env, "cliTempRoot": true, "cliTempRootEnd": true}})
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result struct {
			CLITempRoot struct{ Name string } `json:"cliTempRoot"`
		} `json:"result"`
	}
	if err := json.Unmarshal(cl.call(string(b)), &reply); err != nil || reply.Result.CLITempRoot.Name == "" {
		t.Fatalf("spawn reply %+v %v, want a folder", reply, err)
	}
	if err := os.Chmod(filepath.Join(x, reply.Result.CLITempRoot.Name), 0o777); err != nil {
		t.Fatal(err)
	}
	frames := cl.waitExit("te")
	exit := frames[len(frames)-1]
	if exit.Stream != "exit" || exit.KilledBy != "temp_root" || exit.Signal != "SIGTERM" {
		t.Errorf("exit frame %+v, want SIGTERM and killedBy temp_root", exit)
	}
	waitTempRootsLive(t, 0)
}
