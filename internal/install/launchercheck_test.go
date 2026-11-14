//go:build !windows

package install

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The file check of c9125eb8, on fixtures that the test makes. The owner, the mode
// bits and the file system type come from the facts seam, so the test runs as any
// user: every path is root-owned 0755 on a local disk unless a row names it. The
// texts are copied byte for byte from the Linux captures.

// fakeLauncherFacts installs the facts seam with the given overrides and an own uid
// of 1000.
func fakeLauncherFacts(t *testing.T, over map[string]launcherFacts) {
	t.Helper()
	oldFacts, oldOwn := launcherPathFacts, launcherOwnUID
	launcherPathFacts = func(p string) (launcherFacts, error) {
		if f, ok := over[p]; ok {
			return f, nil
		}
		return launcherFacts{uid: 0, mode: 0o755}, nil
	}
	launcherOwnUID = func() int { return 1000 }
	t.Cleanup(func() { launcherPathFacts, launcherOwnUID = oldFacts, oldOwn })
}

// launcherDir is a temp folder after its links, so a fact override names the path
// that the walk tests (on macOS the temp folder sits behind /var).
func launcherDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func writeLauncher(t *testing.T, p string, body string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func digestOf(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func checkFile(t *testing.T, row, p string, chk LauncherCheck, wantFault, wantReason string) {
	t.Helper()
	f, r := CheckLauncherFile(p, chk)
	if f != wantFault || r != wantReason {
		t.Errorf("%s:\n got %q %q\nwant %q %q", row, f, r, wantFault, wantReason)
	}
}

const zeroDigest = "0000000000000000000000000000000000000000000000000000000000000000"

func writableText(item, mode string) string {
	return "`" + item + "` can be changed by accounts other than its owner (mode " + mode + "), so the launcher can't be trusted. Remove that write access, or keep the launcher somewhere that has none."
}

// TestCheckLauncherFileWritable pins the write test: a folder at 0775, 0757 or 1777,
// and the file at 0775 or 0757. The mode in the text follows. A folder high in the
// path is refused before a lower folder that also fails (measured: /tmp before the
// folders of the login user).
func TestCheckLauncherFileWritable(t *testing.T) {
	d := launcherDir(t)
	L := writeLauncher(t, filepath.Join(d, "a", "b", "launcher.sh"), "#!/bin/sh\n", 0o755)
	a, b := filepath.Join(d, "a"), filepath.Join(d, "a", "b")
	for _, c := range []struct {
		item string
		mode uint32
		text string
	}{
		{b, 0o775, "0775"}, {b, 0o757, "0757"}, {b, 0o1777, "1777"}, {L, 0o775, "0775"}, {L, 0o757, "0757"},
	} {
		fakeLauncherFacts(t, map[string]launcherFacts{c.item: {mode: c.mode}})
		checkFile(t, c.item+" "+c.text, L, LauncherCheck{}, FaultWritable, writableText(c.item, c.text))
	}
	fakeLauncherFacts(t, map[string]launcherFacts{a: {mode: 0o1777}, b: {uid: 1000, mode: 0o755}})
	checkFile(t, "top first", L, LauncherCheck{}, FaultWritable, writableText(a, "1777"))
	// A root file with another group passes: the group is not tested.
	fakeLauncherFacts(t, nil)
	checkFile(t, "root file", L, LauncherCheck{}, "", "")
}

// TestCheckLauncherFileOwner pins the three owner texts, for the file and for a
// folder above it. ownerUid of the launcherCheck lets that uid own the items.
func TestCheckLauncherFileOwner(t *testing.T) {
	d := launcherDir(t)
	L := writeLauncher(t, filepath.Join(d, "x", "launcher.sh"), "#!/bin/sh\n", 0o755)
	x := filepath.Join(d, "x")
	own := func(item string) string {
		return "`" + item + "` belongs to the account that signs in (uid 1000). Root must own the launcher and every folder above it."
	}
	cases := []struct {
		row  string
		over map[string]launcherFacts
		want string
	}{
		{"file own", map[string]launcherFacts{L: {uid: 1000, mode: 0o755}}, own(L)},
		{"folder own", map[string]launcherFacts{x: {uid: 1000, mode: 0o755}}, own(x)},
		{"file 1234", map[string]launcherFacts{L: {uid: 1234, mode: 0o755}}, "`" + L + "` belongs to uid 1234. Root must own the launcher and every folder above it. If you trust uid 1234 with the launcher, you can name it as ownerUid in sshHostLauncher instead."},
		{"file nobody", map[string]launcherFacts{L: {uid: 65534, mode: 0o755}}, "`" + L + "` shows as uid 65534 (nobody), which usually means this login can't see its real owner, as in a sandbox that doesn't map root or on a squashed network export. This login must see root as the owner of the launcher and every folder above it."},
	}
	for _, c := range cases {
		fakeLauncherFacts(t, c.over)
		checkFile(t, c.row, L, LauncherCheck{}, FaultOwner, c.want)
	}
	uid := uint32(1000)
	fakeLauncherFacts(t, nil)
	checkFile(t, "ownerUid, root file", L, LauncherCheck{OwnerUID: &uid}, "", "")
	// The lift for a non-root owner is claustrum's own, not measured.
	fakeLauncherFacts(t, map[string]launcherFacts{L: {uid: 1000, mode: 0o755}})
	checkFile(t, "ownerUid, own file", L, LauncherCheck{OwnerUID: &uid}, "", "")
	fakeLauncherFacts(t, map[string]launcherFacts{L: {uid: 1234, mode: 0o755}})
	checkFile(t, "ownerUid, other uid", L, LauncherCheck{OwnerUID: &uid}, FaultOwner, cases[2].want)
}

// TestCheckLauncherFileNames pins not_absolute, script_suffix and missing.
func TestCheckLauncherFileNames(t *testing.T) {
	fakeLauncherFacts(t, nil)
	d := launcherDir(t)
	checkFile(t, "relative", "launcher.sh", LauncherCheck{}, FaultNotAbsolute, "the launcher must be an absolute path, not a bare name resolved via PATH")
	for _, n := range []string{"x.js", "x.ts", "x.mjs"} {
		p := writeLauncher(t, filepath.Join(d, n), "#\n", 0o755)
		checkFile(t, n, p, LauncherCheck{}, FaultScriptSuffix, "launcher `"+p+"` ends like a JavaScript or TypeScript source file, and no launcher may. Rename it.")
	}
	checkFile(t, "X.JS", writeLauncher(t, filepath.Join(d, "X.JS"), "#\n", 0o755), LauncherCheck{}, "", "")
	missing := func(p string) string {
		return "launcher `" + p + "` does not exist or is not an executable regular file"
	}
	nope := filepath.Join(d, "nope", "launcher.sh")
	checkFile(t, "missing", nope, LauncherCheck{}, FaultMissing, missing(nope))
	dangling := filepath.Join(d, "dangling")
	if err := os.Symlink(filepath.Join(d, "gone"), dangling); err != nil {
		t.Fatal(err)
	}
	checkFile(t, "dangling", dangling, LauncherCheck{}, FaultMissing, missing(dangling))
	checkFile(t, "folder", d, LauncherCheck{}, FaultMissing, missing(d))
	p := writeLauncher(t, filepath.Join(d, "noexec.sh"), "#\n", 0o644)
	noExec := "launcher `" + p + "` is there, but the account that signs in cannot run it (its mode, or a file system mounted noexec)"
	checkFile(t, "0644", p, LauncherCheck{}, FaultMissing, noExec)
	// The execute test comes before the walk: a 0644 file below a 1777 folder gets
	// missing, not writable (c9125eb8, measured on Linux under /tmp).
	fakeLauncherFacts(t, map[string]launcherFacts{d: {mode: 0o1777}})
	checkFile(t, "0644 below 1777", p, LauncherCheck{}, FaultMissing, noExec)
}

// TestCheckLauncherFileDigest pins the digest test: a wrong and a short digest are
// refused, an upper-case digest and a list with the right one pass. An open that fails
// is unreadable with its errno text. 67108864 bytes pass and 67108865 are refused
// without a read.
func TestCheckLauncherFileDigest(t *testing.T) {
	fakeLauncherFacts(t, nil)
	d := launcherDir(t)
	L := writeLauncher(t, filepath.Join(d, "launcher.sh"), "#!/bin/sh\nexec /bin/sleep 2\n", 0o755)
	right := digestOf(t, L)
	none := "launcher `" + L + "` matches none of the SHA-256 digests in the policy. Run sha256sum on the host to see its digest."
	checkFile(t, "right", L, LauncherCheck{SHA256: []string{right}}, "", "")
	checkFile(t, "upper", L, LauncherCheck{SHA256: []string{strings.ToUpper(right)}}, "", "")
	checkFile(t, "two", L, LauncherCheck{SHA256: []string{zeroDigest, right}}, "", "")
	checkFile(t, "wrong", L, LauncherCheck{SHA256: []string{zeroDigest}}, FaultDigest, none)
	checkFile(t, "short", L, LauncherCheck{SHA256: []string{right[:12]}}, FaultDigest, none)

	old := openLauncherFile
	openLauncherFile = func(string) (*os.File, error) { return nil, &os.PathError{Op: "open", Path: L, Err: os.ErrPermission} }
	checkFile(t, "unreadable", L, LauncherCheck{SHA256: []string{right}}, FaultUnreadable, "launcher `"+L+"` cannot be read for its digest: permission denied")
	// Without a digest the file is not read.
	checkFile(t, "unreadable, no digest", L, LauncherCheck{}, "", "")
	openLauncherFile = old

	big := writeLauncher(t, filepath.Join(d, "big"), "#!/bin/sh\n", 0o755)
	if err := os.Truncate(big, launcherDigestMax); err != nil {
		t.Fatal(err)
	}
	checkFile(t, "64 MiB", big, LauncherCheck{SHA256: []string{digestOf(t, big)}}, "", "")
	if err := os.Truncate(big, launcherDigestMax+1); err != nil {
		t.Fatal(err)
	}
	openLauncherFile = func(string) (*os.File, error) { return nil, errors.New("the size test must come before the read") }
	t.Cleanup(func() { openLauncherFile = old })
	checkFile(t, "64 MiB + 1", big, LauncherCheck{SHA256: []string{zeroDigest}}, FaultUnreadable, "launcher `"+big+"` is larger than 64 MiB, the most that can be checked against a digest")
}

// TestCheckLauncherFileNFS pins the rule of an untrusted file system kind: with no digest
// ({} or an empty list) the first item on that file system is refused with its text and
// fault writable. With the right digest it passes, and a wrong digest gives digest. The
// texts follow 4c111e86 (measured on Linux), also those of 9p, of AFS and of an unknown kind.
func TestCheckLauncherFileNFS(t *testing.T) {
	for _, fs := range []string{"NFS", "FUSE or virtiofs", "SMB", "9p", "AFS", "an unknown kind of file system (0x4d44)"} {
		d := launcherDir(t)
		mnt := filepath.Join(d, "mnt")
		L := writeLauncher(t, filepath.Join(mnt, "launcher.sh"), "#!/bin/sh\n", 0o755)
		fakeLauncherFacts(t, map[string]launcherFacts{mnt: {mode: 0o755, fs: fs}, L: {mode: 0o755, fs: fs}})
		text := "`" + mnt + "` is on " + fs + ", so who can change it can't be checked. Keep the launcher and every folder above it on a local disk, such as ext4 or XFS, or in a container's image."
		checkFile(t, fs+" {}", L, LauncherCheck{}, FaultWritable, text)
		checkFile(t, fs+" empty list", L, LauncherCheck{SHA256: []string{}}, FaultWritable, text)
		checkFile(t, fs+" right", L, LauncherCheck{SHA256: []string{digestOf(t, L)}}, "", "")
		checkFile(t, fs+" wrong", L, LauncherCheck{SHA256: []string{zeroDigest}}, FaultDigest, "launcher `"+L+"` matches none of the SHA-256 digests in the policy. Run sha256sum on the host to see its digest.")
	}
}

// TestCheckLauncherFileLike9p pins a trusted disk kind with no real device while a 9p
// mount is listed (4c111e86, measured on Linux with btrfs): with no digest the folder
// gets writable and the 9p text, also with a right ownerUid. With the right digest it passes.
func TestCheckLauncherFileLike9p(t *testing.T) {
	d := launcherDir(t)
	mnt := filepath.Join(d, "btrfs")
	L := writeLauncher(t, filepath.Join(mnt, "launcher.sh"), "#!/bin/sh\n", 0o755)
	fakeLauncherFacts(t, map[string]launcherFacts{mnt: {mode: 0o755, like9p: true}, L: {mode: 0o755, like9p: true}})
	text := "`" + mnt + "` can't be told apart from a 9p share that is mounted on this machine (`findmnt -t 9p` lists it, unless it's outside this container), so who can change it can't be checked. Unmount the share, or keep the launcher and every folder above it on a local disk, such as ext4 or XFS."
	uid := uint32(0)
	checkFile(t, "like 9p {}", L, LauncherCheck{}, FaultWritable, text)
	checkFile(t, "like 9p ownerUid", L, LauncherCheck{OwnerUID: &uid}, FaultWritable, text)
	checkFile(t, "like 9p right", L, LauncherCheck{SHA256: []string{digestOf(t, L)}}, "", "")
}

// TestCheckLauncherFileOwnFUSE pins a FUSE mount that the uid of the daemon owns
// (4c111e86, measured on Linux). With no digest it gets writable and the FUSE text, also
// with that uid as ownerUid. With the right digest it gets owner.
func TestCheckLauncherFileOwnFUSE(t *testing.T) {
	d := launcherDir(t)
	mnt := filepath.Join(d, "fusemnt")
	L := writeLauncher(t, filepath.Join(mnt, "launcher.sh"), "#!/bin/sh\n", 0o755)
	fakeLauncherFacts(t, map[string]launcherFacts{mnt: {uid: 1000, mode: 0o755, fs: "FUSE or virtiofs"}, L: {uid: 1000, mode: 0o755, fs: "FUSE or virtiofs"}})
	fuse := "`" + mnt + "` is on FUSE or virtiofs, so who can change it can't be checked. Keep the launcher and every folder above it on a local disk, such as ext4 or XFS, or in a container's image."
	uid := uint32(1000)
	checkFile(t, "own FUSE {}", L, LauncherCheck{}, FaultWritable, fuse)
	checkFile(t, "own FUSE ownerUid", L, LauncherCheck{OwnerUID: &uid}, FaultWritable, fuse)
	checkFile(t, "own FUSE right digest", L, LauncherCheck{SHA256: []string{digestOf(t, L)}}, FaultOwner,
		"`"+mnt+"` belongs to the account that signs in (uid 1000). Root must own the launcher and every folder above it.")
}

// TestCheckLauncherFileLinks pins that a link to a good launcher and a chain of five
// links pass. The walk also tests the folders after the links: a writable folder of
// the target refuses a link to it (claustrum's own, not measured).
func TestCheckLauncherFileLinks(t *testing.T) {
	d := launcherDir(t)
	ok := writeLauncher(t, filepath.Join(d, "ok", "launcher.sh"), "#!/bin/sh\n", 0o755)
	prev := ok
	for i := range 5 {
		next := filepath.Join(d, "chain"+string(rune('0'+i)))
		if err := os.Symlink(prev, next); err != nil {
			t.Fatal(err)
		}
		prev = next
	}
	fakeLauncherFacts(t, nil)
	checkFile(t, "chain of 5", prev, LauncherCheck{SHA256: []string{digestOf(t, ok)}}, "", "")
	fakeLauncherFacts(t, map[string]launcherFacts{filepath.Join(d, "ok"): {mode: 0o777}})
	checkFile(t, "target folder", prev, LauncherCheck{}, FaultWritable, writableText(filepath.Join(d, "ok"), "0777"))
}

// TestCheckLauncherFileDigestGrowth pins the bound of the read: a file that grows
// after the stat is refused as too large and the read stops at the limit (claustrum's
// own, not measured). The seam opens an endless source for a small file.
func TestCheckLauncherFileDigestGrowth(t *testing.T) {
	d := launcherDir(t)
	L := writeLauncher(t, filepath.Join(d, "g", "launcher.sh"), "#!/bin/sh\n", 0o755)
	fakeLauncherFacts(t, nil)
	old := openLauncherFile
	openLauncherFile = func(string) (*os.File, error) { return os.Open("/dev/zero") }
	t.Cleanup(func() { openLauncherFile = old })
	checkFile(t, "grown", L, LauncherCheck{SHA256: []string{zeroDigest}}, FaultUnreadable,
		"launcher `"+L+"` is larger than 64 MiB, the most that can be checked against a digest")
}

// TestCheckLauncherFileLinkHolder pins the test of the folder that holds a middle link
// of a chain: front, then w/mid, then the file in ok. w is on neither path, so only the
// link walk reaches it (claustrum's own, not measured).
func TestCheckLauncherFileLinkHolder(t *testing.T) {
	d := launcherDir(t)
	ok := writeLauncher(t, filepath.Join(d, "ok", "launcher.sh"), "#!/bin/sh\n", 0o755)
	w := filepath.Join(d, "w")
	if err := os.MkdirAll(w, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ok, filepath.Join(w, "mid")); err != nil {
		t.Fatal(err)
	}
	front := filepath.Join(d, "front")
	if err := os.Symlink(filepath.Join(w, "mid"), front); err != nil {
		t.Fatal(err)
	}
	fakeLauncherFacts(t, nil)
	checkFile(t, "root-owned holder", front, LauncherCheck{}, "", "")
	fakeLauncherFacts(t, map[string]launcherFacts{w: {mode: 0o777}})
	checkFile(t, "writable holder", front, LauncherCheck{}, FaultWritable, writableText(w, "0777"))
}
