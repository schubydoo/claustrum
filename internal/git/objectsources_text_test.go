package git

import "testing"

// The texts of the object source checks, byte for byte against the frames of
// b3081b51 on Windows (the message member). The paths are neutral fixture paths.
func TestObjectSourceTexts(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"Windows line endings", alternatesCRLFText("C:\\work\\R\\.git\\objects\\info\\alternates"),
			"`C:\\work\\R\\.git\\objects\\info\\alternates` has Windows line endings, which git reads as part of each path, so git wasn't run. Save that file with Unix line endings."},
		{"a quoted line", alternatesQuoteText("C:\\work\\R\\.git\\objects\\info\\alternates"),
			"`C:\\work\\R\\.git\\objects\\info\\alternates` has a quoted line, which Claude doesn't support, so git wasn't run. To stop depending on that file, run \"git repack -a -d\" in the repository it belongs to, then delete that file."},
		{"a control character", alternatesControlText("C:\\work\\R\\.git\\objects\\info\\alternates"),
			"`C:\\work\\R\\.git\\objects\\info\\alternates` has a line with a control character, which git takes as part of the path, so git wasn't run. To stop depending on that file, run \"git repack -a -d\" in the repository it belongs to, then delete that file."},
		{"a dot dot after a link", alternatesDotDotText("C:\\work\\R\\.git\\objects\\info\\alternates", "C:\\work\\lnk"),
			"`C:\\work\\R\\.git\\objects\\info\\alternates` holds a path in which \"..\" comes after a symlink or junction (`C:\\work\\lnk`), which Windows and git resolve differently, so git wasn't run. Rewrite that path as a full path without \"..\"."},
		{"a file over 1 MiB", alternatesUnreadableText("C:\\work\\R\\.git\\objects\\info\\alternates"),
			"`C:\\work\\R\\.git\\objects\\info\\alternates` couldn't be read as a regular file of at most 1 MiB, so git wasn't run. Check that file and its permissions."},
		{"a folder", alternatesUnreadableText("C:\\work\\R\\.git\\objects\\info\\alternates"),
			"`C:\\work\\R\\.git\\objects\\info\\alternates` couldn't be read as a regular file of at most 1 MiB, so git wasn't run. Check that file and its permissions."},
		{"1000 lines", alternatesTooManyText("C:\\work\\R\\.git\\objects\\info\\alternates"),
			"`C:\\work\\R\\.git\\objects\\info\\alternates` lists more object directories than Claude checks (at most 64 in total, nested at most 6 deep), so git wasn't run. To stop depending on that file, run \"git repack -a -d\" in the repository it belongs to, then delete that file."},
		{"a chain of 20", alternatesTooManyText("C:\\work\\ch\\6\\objects\\info\\alternates"),
			"`C:\\work\\ch\\6\\objects\\info\\alternates` lists more object directories than Claude checks (at most 64 in total, nested at most 6 deep), so git wasn't run. To stop depending on that file, run \"git repack -a -d\" in the repository it belongs to, then delete that file."},
		{"the environment", alternateEnvText,
			"The remote machine's environment sets GIT_ALTERNATE_OBJECT_DIRECTORIES, which Claude can't use on a Windows host, so git wasn't run. Remove it from the environment, then restart the machine."},
		{"a chain of 32 links", linkChainText("C:\\work\\l32"),
			"Claude couldn't check that `C:\\work\\l32` stays on a local drive (too many links, or a loop of links), so git wasn't run. If a folder name in that path ends in a dot or a space, rename it. Otherwise, replace the link in that path with the folder it points to."},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, c.got, c.want)
		}
	}
}
