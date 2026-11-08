package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPluginsPruneSkipFrames pins the whole frames of the c9125eb8 skips. Each case
// boots this daemon at run/<own>/rpc.sock, and a live sibling where the row had one.
// The staging and the frames are those of the rows: Linux C13 and C13B, macOS C13b
// (f5). <root> stands for the per-install root. Each case also checks which plugin
// directories stay.
func TestPluginsPruneSkipFrames(t *testing.T) {
	// The Linux staging: four hash dirs in each root, one of them young.
	linuxPerInstall := []string{"0123456789abcdef", "3333333333333333", "aaaaaaaaaaaaaaaa", "fedcba9876543210"}
	linuxLegacy := []string{"0123456789abcdef", "2222222222222222", "aaaaaaaaaaaaaaaa", "fedcba9876543210"}
	linuxYoung := map[string]bool{"3333333333333333": true, "2222222222222222": true}
	// The macOS f5 staging: two old hash dirs in each root.
	macPerInstall := []string{"0123456789abcdef", "0123456789abcdf0"}
	macLegacy := []string{"0123456789abce53", "0123456789abce54"}

	const hostTermText = "machines share this home (this daemon's run directory bears a host term), and another machine's sessions cannot be seen from here"
	hostTermFrame := `{"jsonrpc":"2.0","id":1,"result":{"root":<root>,"pruned":[],"prunedLegacy":[],"staleArchives":0,"kept":0,"live":0,"young":4,"legacy":"skipped: ` + hostTermText + `","skipped":"` + hostTermText + `","minAgeDays":7}}`
	type skipCase struct {
		name, own          string
		siblings           []string
		perInstall, legacy []string
		young              map[string]bool
		frame              string
		gone               []string // per-install hashes that the prune removes
	}
	cases := []skipCase{
		{
			name: "C91 sibling text", own: "a1b2c3d4", siblings: []string{"e5f6a7b8"},
			perInstall: linuxPerInstall, legacy: linuxLegacy, young: linuxYoung,
			frame: `{"jsonrpc":"2.0","id":1,"result":{"root":<root>,"pruned":["0123456789abcdef","aaaaaaaaaaaaaaaa","fedcba9876543210"],"prunedLegacy":[],"staleArchives":0,"kept":0,"live":0,"young":1,"legacy":"skipped: another daemon is running (run/e5f6a7b8/rpc.sock answers); its sessions may use the shared legacy dirs","minAgeDays":7}}`,
			gone:  []string{"0123456789abcdef", "aaaaaaaaaaaaaaaa", "fedcba9876543210"},
		},
		{
			name: "host term", own: "a1b2c3d4-ffffffff",
			perInstall: linuxPerInstall, legacy: linuxLegacy, young: linuxYoung,
			frame: hostTermFrame,
		},
		{
			name: "host term with a live sibling", own: "a1b2c3d4-ffffffff", siblings: []string{"e5f6a7b8"},
			perInstall: linuxPerInstall, legacy: linuxLegacy, young: linuxYoung,
			frame: hostTermFrame,
		},
		{
			name: "not under a run directory of an install", own: "a1b2c3d4-ffff",
			perInstall: linuxPerInstall, legacy: linuxLegacy, young: linuxYoung,
			frame: `{"jsonrpc":"2.0","id":1,"result":{"root":"","pruned":[],"prunedLegacy":[],"staleArchives":0,"kept":0,"live":0,"young":0,"legacy":"skipped: this daemon's socket is not under a run directory of an install, so it has no per-install plugin root to prune","skipped":"this daemon's socket is not under a run directory of an install, so it has no per-install plugin root to prune","minAgeDays":0}}`,
		},
		{
			name: "a sibling of another name shape", own: "abcd1234", siblings: []string{"beef5678-host"},
			perInstall: macPerInstall, legacy: macLegacy,
			frame: `{"jsonrpc":"2.0","id":1,"result":{"root":<root>,"pruned":[],"prunedLegacy":[],"staleArchives":0,"kept":0,"live":0,"young":2,"legacy":"skipped: another daemon of this install may be running (run/beef5678-host/rpc.sock answers); its sessions may use the same dirs","skipped":"another daemon of this install may be running (run/beef5678-host/rpc.sock answers); its sessions may use the same dirs","minAgeDays":7}}`,
		},
		{
			// An 8 hex sibling comes first in name order. The later sibling of another
			// shape still spares both roots, and the text names it (claustrum's own,
			// not measured).
			name: "a sibling of another name shape after an 8 hex sibling", own: "abcd1234", siblings: []string{"00000001", "abcd1234-host"},
			perInstall: macPerInstall, legacy: macLegacy,
			frame: `{"jsonrpc":"2.0","id":1,"result":{"root":<root>,"pruned":[],"prunedLegacy":[],"staleArchives":0,"kept":0,"live":0,"young":2,"legacy":"skipped: another daemon of this install may be running (run/abcd1234-host/rpc.sock answers); its sessions may use the same dirs","skipped":"another daemon of this install may be running (run/abcd1234-host/rpc.sock answers); its sessions may use the same dirs","minAgeDays":7}}`,
		},
	}
	// A live sibling of the host-term shape, as in the macOS and Windows rows: own
	// a1b2c3d4, sibling beef5678-<machine-id first 8>. The root of this install is
	// pruned and only the shared root stays. `young` counts this root only.
	const termSib = "beef5678-0badf00d"
	termPerInstallYoung := append([]string{"3333333333333333"}, macPerInstall...)
	termLegacyYoung := append([]string{"2222222222222222"}, macLegacy...)
	termYoung := map[string]bool{"3333333333333333": true, "2222222222222222": true}
	termFrame := func(young int) string {
		return `{"jsonrpc":"2.0","id":1,"result":{"root":<root>,"pruned":["0123456789abcdef","0123456789abcdf0"],"prunedLegacy":[],"staleArchives":0,"kept":0,"live":0,"young":` + strconv.Itoa(young) + `,"legacy":"skipped: another daemon is running (run/` + termSib + `/rpc.sock answers); its sessions may use the shared legacy dirs","minAgeDays":7}}`
	}
	cases = append(cases,
		skipCase{
			name: "a sibling of the host-term shape", own: "a1b2c3d4", siblings: []string{termSib},
			perInstall: macPerInstall, legacy: macLegacy,
			frame: termFrame(0), gone: macPerInstall,
		},
		skipCase{
			name: "a sibling of the host-term shape, a young dir in this root", own: "a1b2c3d4", siblings: []string{termSib},
			perInstall: termPerInstallYoung, legacy: macLegacy, young: termYoung,
			frame: termFrame(1), gone: macPerInstall,
		},
		skipCase{
			name: "a sibling of the host-term shape, a young dir in the shared root", own: "a1b2c3d4", siblings: []string{termSib},
			perInstall: macPerInstall, legacy: termLegacyYoung, young: termYoung,
			frame: termFrame(0), gone: macPerInstall,
		},
		skipCase{
			// A host-term sibling of the own client id uses this same root. It spares
			// both roots (measured on Linux. On macOS and Windows: claustrum's own,
			// not measured).
			name: "a sibling of the host-term shape that shares this root", own: "a1b2c3d4", siblings: []string{"a1b2c3d4-0badf00d"},
			perInstall: macPerInstall, legacy: macLegacy,
			frame: `{"jsonrpc":"2.0","id":1,"result":{"root":<root>,"pruned":[],"prunedLegacy":[],"staleArchives":0,"kept":0,"live":0,"young":2,"legacy":"skipped: another daemon of this install may be running (run/a1b2c3d4-0badf00d/rpc.sock answers); its sessions may use the same dirs","skipped":"another daemon of this install may be running (run/a1b2c3d4-0badf00d/rpc.sock answers); its sessions may use the same dirs","minAgeDays":7}}`,
		},
	)
	// The Linux rows of c9125eb8 with host-term siblings: own abcd1234, three old hash
	// dirs in each root. A sibling of another client id lets the prune remove the dirs
	// of this root, also with a foreign term or a young dir in the shared root. A
	// sibling of the own client id keeps both roots. The frames are those of the rows,
	// with a made-up host term.
	xHashes := []string{"0123456789abcdef", "aaaaaaaaaaaaaaaa", "fedcba9876543210"}
	xLegacyYoung := append([]string{"2222222222222222"}, xHashes...)
	xOtherID := func(sib string) string {
		return `{"jsonrpc":"2.0","id":1,"result":{"root":<root>,"pruned":["0123456789abcdef","aaaaaaaaaaaaaaaa","fedcba9876543210"],"prunedLegacy":[],"staleArchives":0,"kept":0,"live":0,"young":0,"legacy":"skipped: another daemon is running (run/` + sib + `/rpc.sock answers); its sessions may use the shared legacy dirs","minAgeDays":7}}`
	}
	xOwnID := func(sib string) string {
		reason := "another daemon of this install may be running (run/" + sib + "/rpc.sock answers); its sessions may use the same dirs"
		return `{"jsonrpc":"2.0","id":1,"result":{"root":<root>,"pruned":[],"prunedLegacy":[],"staleArchives":0,"kept":0,"live":0,"young":3,"legacy":"skipped: ` + reason + `","skipped":"` + reason + `","minAgeDays":7}}`
	}
	cases = append(cases,
		skipCase{
			name: "another client id, host term", own: "abcd1234", siblings: []string{"beef5678-1234abcd"},
			perInstall: xHashes, legacy: xHashes,
			frame: xOtherID("beef5678-1234abcd"), gone: xHashes,
		},
		skipCase{
			name: "another client id, foreign term", own: "abcd1234", siblings: []string{"beef5678-ffffffff"},
			perInstall: xHashes, legacy: xHashes,
			frame: xOtherID("beef5678-ffffffff"), gone: xHashes,
		},
		skipCase{
			name: "another client id, a young dir in the shared root", own: "abcd1234", siblings: []string{"beef5678-1234abcd"},
			perInstall: xHashes, legacy: xLegacyYoung, young: map[string]bool{"2222222222222222": true},
			frame: xOtherID("beef5678-1234abcd"), gone: xHashes,
		},
		skipCase{
			name: "the own client id, host term", own: "abcd1234", siblings: []string{"abcd1234-1234abcd"},
			perInstall: xHashes, legacy: xHashes,
			frame: xOwnID("abcd1234-1234abcd"),
		},
		skipCase{
			name: "the own client id, foreign term", own: "abcd1234", siblings: []string{"abcd1234-ffffffff"},
			perInstall: xHashes, legacy: xHashes,
			frame: xOwnID("abcd1234-ffffffff"),
		},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A short temp root, not t.TempDir(): the socket path must stay under the
			// macOS sockaddr_un limit (~104 bytes).
			base, err := os.MkdirTemp("", "cl")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(base) })
			listen := func(name string) net.Listener {
				d := filepath.Join(base, "run", name)
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
				ln, err := net.Listen("unix", filepath.Join(d, "rpc.sock"))
				if err != nil {
					t.Skipf("AF_UNIX listen unsupported here: %v", err)
				}
				t.Cleanup(func() { _ = ln.Close() })
				return ln
			}
			ln := listen(c.own)
			for _, sib := range c.siblings {
				listen(sib)
			}

			perInstall := filepath.Join(base, "plugins", c.own[:8])
			legacyRoot := filepath.Join(base, "plugins")
			old := time.Now().Add(-40 * 24 * time.Hour)
			seed := func(root string, hashes []string) {
				for _, h := range hashes {
					p := filepath.Join(root, h)
					if err := os.MkdirAll(p, 0o755); err != nil {
						t.Fatal(err)
					}
					if !c.young[h] {
						if err := os.Chtimes(p, old, old); err != nil {
							t.Fatal(err)
						}
					}
				}
				// Entries that no prune touches: a file and a dir of another name.
				if err := os.MkdirAll(filepath.Join(root, "claude-aaaaaaaaaaaa"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "keep-me"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			seed(perInstall, c.perInstall)
			seed(legacyRoot, c.legacy)

			s := &server{ln: ln, procs: newTestProcManager(t)}
			resp := s.pluginsPrune(&request{ID: 1, Params: json.RawMessage(`{}`)})
			got, err := json.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}
			rootJSON, err := json.Marshal(perInstall)
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.ReplaceAll(c.frame, "<root>", string(rootJSON)); string(got) != want {
				t.Errorf("frame:\n got %s\nwant %s", got, want)
			}

			gone := map[string]bool{}
			for _, h := range c.gone {
				gone[h] = true
			}
			for _, h := range c.perInstall {
				_, err := os.Stat(filepath.Join(perInstall, h))
				if gone[h] != os.IsNotExist(err) {
					t.Errorf("%s in the per-install root: gone=%v, want gone=%v", h, os.IsNotExist(err), gone[h])
				}
			}
			for _, h := range c.legacy {
				if _, err := os.Stat(filepath.Join(legacyRoot, h)); err != nil {
					t.Errorf("%s in the shared root must stay: %v", h, err)
				}
			}
		})
	}
}
