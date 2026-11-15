package git

import "testing"

// TestRoomWords: the sizes of the text of worktree_lfs_no_space. The free
// space rounds down and the fetched size up (4c111e86, measured on Linux). The
// bounds of 1 MiB and 1 GiB, the rounding of KiB, and a free space of 1 GiB or
// more are claustrum's own.
func TestRoomWords(t *testing.T) {
	for _, row := range []struct {
		n    uint64
		up   bool
		want string
	}{
		{21413888, false, "20 MiB"},
		{19083264, false, "18 MiB"},
		{409231360, false, "390 MiB"},
		{207388672, true, "198 MiB"},
		{209715216, true, "201 MiB"},
		{3717267456, true, "3.5 GiB"},
		{4096, false, "4 KiB"},
		{0, false, "0 KiB"},
		{1 << 30, true, "1.0 GiB"},
		{3717267456, false, "3.4 GiB"},
	} {
		if got := roomWords(row.n, row.up); got != row.want {
			t.Errorf("roomWords(%d, %v) = %q, want %q", row.n, row.up, got, row.want)
		}
	}
}

// TestLFSRoomText: the whole text of worktree_lfs_no_space (4c111e86,
// measured on Linux).
func TestLFSRoomText(t *testing.T) {
	got := lfsRoomText(&lfsRoomStop{free: 409231360, fetched: 3717267456, runDir: "/home/u/.claude/remote/run/c1"})
	want := "Claude stopped the checkout when only 390 MiB was left free on the disk that holds /home/u/.claude/remote/run/c1 on the remote machine." +
		" The worktree and the 3.5 GiB of Git LFS files already fetched into that folder were removed." +
		" Free up space on that disk, or turn off Worktree for this session, then try again." +
		" Claude keeps a checkout's Git LFS files in that folder until the checkout ends, so they need room on that disk."
	if got != want {
		t.Errorf("lfsRoomText = %q\nwant %q", got, want)
	}
}
