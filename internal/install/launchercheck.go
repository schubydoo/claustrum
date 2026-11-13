package install

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The file check of a launcherCheck (c9125eb8). process.spawn and launcher.resolve
// run it on a launcher when the request carries a launcherCheck. Only Linux runs it:
// macOS and Windows refuse first (LauncherCheckHostRefusal). Every text is copied byte
// for byte from a capture of c9125eb8 on Linux, unless its comment names 4c111e86 or
// says "claustrum's own, not measured".

// LauncherCheck is the launcherCheck member of process.spawn and launcher.resolve. sha256
// is a list of digests (a string gets Invalid params, measured on Windows). ownerUid
// names one more uid that can own the launcher and its folders. A root-owned launcher
// passes with it (measured on Linux). The lift for that uid is claustrum's own, not
// measured. path is in the shape (measured on Windows). claustrum
// reads it for the shape only, and the check tests the launcher itself (claustrum's
// own, not measured).
type LauncherCheck struct {
	SHA256   []string `json:"sha256"`
	OwnerUID *uint32  `json:"ownerUid"`
	Path     string   `json:"path"`
}

// The launcherFault values of a refusal.
const (
	FaultWritable     = "writable"
	FaultOwner        = "owner"
	FaultScriptSuffix = "script_suffix"
	FaultMissing      = "missing"
	FaultLinks        = "links"
	FaultUnreadable   = "unreadable"
	FaultDigest       = "digest"
	FaultNotAbsolute  = "not_absolute"
	FaultMacOSHost    = "macos_host"
	FaultWindowsHost  = "windows_host"
)

// launcherDigestMax is the largest launcher that the digest test reads: 67108864 bytes
// pass, 67108865 bytes are refused.
const launcherDigestMax = 64 << 20

// nobodyUID is the uid that a login sees for an owner that it cannot map.
const nobodyUID = 65534

// NoLauncherText is the reason of an empty launcher array.
const NoLauncherText = managedNoLauncher

// launcherFacts are the facts of one path that the walk tests: its owner, its mode
// bits (the 12 low bits of st_mode) and fs. fs names a file system whose server decides
// who can change a file, as the refusal text shows it: "NFS", "FUSE or virtiofs" or
// "SMB". It is "" for any other file system.
type launcherFacts struct {
	uid  uint32
	mode uint32
	fs   string
}

// launcherPathFacts and launcherOwnUID are seams, so a test fakes the owner, the mode
// and the file system type, and runs as any user. Production never reassigns them.
var (
	launcherPathFacts = statLauncherFacts
	launcherOwnUID    = os.Geteuid
	// openLauncherFile opens the launcher for its digest. A seam, so a test makes the
	// open fail without a mode-0711 file (root reads one).
	openLauncherFile = os.Open
)

// CheckLauncherFile runs the file check on the launcher p. It returns the fault and
// the reason text, or two empty strings when the launcher passes. The order is
// claustrum's own, not measured, except where a row shows two faults of one launcher:
//
//  1. A relative p is not_absolute.
//
//  2. A name with a script ending is script_suffix (the endings of Check). X.JS passes.
//
//  3. A p that leads through too many links, or a loop, is links (a chain of 41, a loop
//     of 2 and a self loop are measured with a digest, a chain of 40 passes). The system call gives
//     ELOOP there, and a link loop in a folder of p gets the same fault (claustrum's
//     own, not measured). A p that is missing, a dangling link or no regular file is
//     missing.
//
//  4. A launcher that this account cannot run is missing. This test comes before the
//     walk: a mode-0644 file under /tmp got missing, not writable for /tmp (measured).
//
//  5. The walk: "/", each folder down to the launcher, then the launcher. It walks the
//     folders of p as written, then the folders of p after its links (claustrum's own,
//     not measured). Each item runs the file system test, then the owner test, then the write
//     test (that order is claustrum's own). A launcher under /tmp is refused for /tmp,
//     before its own folders (measured).
//
//  6. With digests: the size limit, the read, and the compare. {} and an empty list
//     pass (measured). Without digests the file is not read (claustrum's own, not
//     measured).
//
//  7. On Linux, the check by handle (launcherMoved). A folder or a file that changes
//     after it, before the start, is not guarded (claustrum's own, not measured).
func CheckLauncherFile(p string, chk LauncherCheck) (fault, reason string) {
	shown := managedShown(p)
	switch {
	case !filepath.IsAbs(p):
		return FaultNotAbsolute, managedNotAbsolute
	case hasManagedScriptExt(p):
		return FaultScriptSuffix, "launcher `" + shown + "` ends like a JavaScript or TypeScript source file, and no launcher may. Rename it."
	}
	fi, err := os.Stat(p)
	if errors.Is(err, syscall.ELOOP) {
		return FaultLinks, "too many symbolic links (or a loop) lead to launcher `" + shown + "`"
	}
	if err != nil || !fi.Mode().IsRegular() {
		return FaultMissing, managedNotExecutable(p)
	}
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return FaultMissing, managedNotExecutable(p)
	}
	look, err := lookLauncherPath(target)
	if err != nil {
		return FaultMissing, managedNotExecutable(p)
	}
	if !IsExecutableRegular(p) {
		return FaultMissing, "launcher `" + shown + "` is there, but the account that signs in cannot run it (its mode, or a file system mounted noexec)"
	}
	digests := len(chk.SHA256) > 0
	seen := map[string]bool{}
	items := launcherFolders(filepath.Dir(filepath.Clean(p)))
	// The folder that holds each link of the chain is tested too, so a link that sits
	// in a folder another account can write is refused (claustrum's own, not measured:
	// the measured link rows hold their links in a root-owned folder).
	for _, d := range linkHolderFolders(p) {
		items = append(items, launcherFolders(d)...)
	}
	items = append(items, append(launcherFolders(filepath.Dir(target)), target)...)
	for _, item := range items {
		if seen[item] {
			continue
		}
		seen[item] = true
		if f, r := checkLauncherItem(p, item, chk.OwnerUID, digests); f != "" {
			return f, r
		}
	}
	if digests {
		if f, r := checkLauncherDigest(target, shown, fi.Size(), chk.SHA256); f != "" {
			return f, r
		}
	}
	return launcherMoved(target, look)
}

// linkHolderFolders walks the absolute path p one name at a time, as the system does,
// and returns the real folder that holds each link it meets, in the folders and in
// the chain of the last name. It stops after 255 links or on a read error: the
// EvalSymlinks call before it has already refused such a path.
func linkHolderFolders(p string) []string {
	var out []string
	resolved := string(filepath.Separator)
	rest := strings.Split(p, string(filepath.Separator))
	for hops := 0; len(rest) > 0; {
		name := rest[0]
		rest = rest[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, name)
		fi, err := os.Lstat(next)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		hops++
		dest, err := os.Readlink(next)
		if hops > 255 || err != nil {
			return out
		}
		out = append(out, resolved)
		if filepath.IsAbs(dest) {
			resolved = string(filepath.Separator)
		}
		rest = append(strings.Split(dest, string(filepath.Separator)), rest...)
	}
	return out
}

// launcherFolders is "/" and each folder from the top down to dir.
func launcherFolders(dir string) []string {
	var out []string
	for d := dir; ; d = filepath.Dir(d) {
		out = append(out, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// checkLauncherItem tests one item of the walk. Root owns a good item, or the
// ownerUid of the launcherCheck does. A group or other write bit refuses it, the
// sticky folder 1777 too. On NFS, FUSE or SMB with no digest the item is refused,
// whatever its owner and mode. A FUSE mount of the uid of the daemon gets writable
// there, also with that uid as ownerUid. With a digest the owner test still
// runs, and that mount gets owner (both rules: 4c111e86, measured on Linux). The write test
// there is claustrum's own, not measured. An item whose
// facts cannot be read is missing (claustrum's own, not measured).
func checkLauncherItem(launcher, item string, ownerUID *uint32, digests bool) (fault, reason string) {
	f, err := launcherPathFacts(item)
	shown := "`" + managedShown(item) + "`"
	if err != nil {
		return FaultMissing, managedNotExecutable(launcher)
	}
	if f.fs != "" && !digests {
		// The NFS and the FUSE text follow 4c111e86 (measured on Linux). The SMB text
		// uses the same words with its own name (claustrum's own, not measured).
		return FaultWritable, shown + " is on " + f.fs + ", so who can change it can't be checked. Keep the launcher and every folder above it on a local disk, such as ext4 or XFS, or in a container's image."
	}
	if f.uid != 0 && (ownerUID == nil || f.uid != *ownerUID) {
		// The order of the three texts, for a daemon that runs as uid 65534, is
		// claustrum's own, not measured. The first text follows 4c111e86 (measured on
		// Linux, on a FUSE mount).
		switch {
		case int64(f.uid) == int64(launcherOwnUID()):
			return FaultOwner, fmt.Sprintf("%s belongs to the account that signs in (uid %d). Root must own the launcher and every folder above it.", shown, f.uid)
		case f.uid == nobodyUID:
			return FaultOwner, shown + " shows as uid 65534 (nobody), which usually means this login can't see its real owner, as in a sandbox that doesn't map root or on a squashed network export. This login must see root as the owner of the launcher and every folder above it."
		default:
			return FaultOwner, fmt.Sprintf("%s belongs to uid %d. Root must own the launcher and every folder above it. If you trust uid %d with the launcher, you can name it as ownerUid in sshHostLauncher instead.", shown, f.uid, f.uid)
		}
	}
	if f.mode&0o022 != 0 {
		return FaultWritable, fmt.Sprintf("%s can be changed by accounts other than its owner (mode %04o), so the launcher can't be trusted. Remove that write access, or keep the launcher somewhere that has none.", shown, f.mode)
	}
	return "", ""
}

// checkLauncherDigest reads the launcher at target and compares its SHA-256 with each
// digest, in any letter case. A file over the limit is not read. The read stops one
// byte past the limit, so a file that grows after the stat is refused too.
func checkLauncherDigest(target, shown string, size int64, want []string) (fault, reason string) {
	tooBig := "launcher `" + shown + "` is larger than 64 MiB, the most that can be checked against a digest"
	if size > launcherDigestMax {
		return FaultUnreadable, tooBig
	}
	f, err := openLauncherFile(target)
	if err != nil {
		return FaultUnreadable, "launcher `" + shown + "` cannot be read for its digest: " + managedErrText(err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, launcherDigestMax+1))
	if err != nil {
		return FaultUnreadable, "launcher `" + shown + "` cannot be read for its digest: " + managedErrText(err)
	}
	if n > launcherDigestMax {
		return FaultUnreadable, tooBig
	}
	got := hex.EncodeToString(h.Sum(nil))
	for _, d := range want {
		if strings.EqualFold(d, got) {
			return "", ""
		}
	}
	return FaultDigest, "launcher `" + shown + "` matches none of the SHA-256 digests in the policy. Run sha256sum on the host to see its digest."
}
